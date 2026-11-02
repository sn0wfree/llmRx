package clientapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/model"
)

func newTestIngestor(t *testing.T, clients []EdgeClient) (*Ingestor, *logstore.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	ls, err := logstore.New(dir, logstore.NewJSONLDriver())
	if err != nil {
		t.Fatalf("logstore.New: %v", err)
	}
	t.Cleanup(func() { _ = ls.Close() })
	name := ""
	if len(clients) > 0 {
		name = clients[0].Name
	}
	return NewIngestor(ls, clients), ls, name
}

func postUsage(t *testing.T, ing *Ingestor, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/client/v1/usage", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ing.Handler().ServeHTTP(rec, req)
	return rec
}

func TestUsageIngest_AcceptsAndPersists(t *testing.T) {
	ing, ls, _ := newTestIngestor(t, []EdgeClient{{Name: "edge-1", Token: "tok-1"}})

	body := `{"client_name":"liar","records":[
		{"request_id":"r1","ts":1761900000,"model":"gpt-4","status":200,
		 "prompt_tokens":10,"completion_tokens":5,"cost_usd":0.01,
		 "source":"proxy","latency_ms":120}]}`
	rec := postUsage(t, ing, "tok-1", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	// The row lands in the shared log store, attributed to the
	// CONFIGURED name (the body's self-report is ignored).
	ls.Flush()
	rows, total, err := ls.Query(logstore.QueryFilter{Limit: 10}, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
	if rows[0].RouterPath != "client:edge-1" {
		t.Fatalf("RouterPath = %q, want client:edge-1", rows[0].RouterPath)
	}
	if rows[0].Model != "gpt-4" || rows[0].PromptTokens != 10 {
		t.Fatalf("row = %+v", rows[0])
	}
}

func TestUsageIngest_Auth(t *testing.T) {
	ing, _, _ := newTestIngestor(t, []EdgeClient{{Name: "edge-1", Token: "tok-1"}})

	if rec := postUsage(t, ing, "", `{"records":[]}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", rec.Code)
	}
	if rec := postUsage(t, ing, "wrong", `{"records":[]}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: %d, want 401", rec.Code)
	}
	if rec := postUsage(t, ing, "tok-1", `{"records":[{"request_id":"x","source":"proxy"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("good token: %d", rec.Code)
	}
}

// Billable-vs-observability is the whole point of the source field: a
// "local" row is the only record of spend the server never saw.
func TestUsageIngest_LocalRowsAreBillable(t *testing.T) {
	ing, ls, _ := newTestIngestor(t, []EdgeClient{{Name: "edge-1", Token: "t"}})

	body := `{"records":[
		{"request_id":"p1","source":"proxy","model":"m","cost_usd":0.5,"status":200},
		{"request_id":"c1","source":"cache","model":"m","cost_usd":0,"status":200},
		{"request_id":"l1","source":"local","model":"m","cost_usd":2.0,"status":200}]}`
	if rec := postUsage(t, ing, "t", body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	ls.Flush()
	rows, total, err := ls.Query(logstore.QueryFilter{Limit: 10}, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3 (all sources are recorded)", total)
	}
	// The local row's cost is the only server-visible spend from that
	// request class; verify it round-tripped.
	var localCost float64
	for _, r := range rows {
		if r.RouterPath == "client:edge-1" && r.RealCostUSD > 1 {
			localCost = r.RealCostUSD
		}
	}
	if localCost != 2.0 {
		t.Fatalf("local row cost = %v, want 2.0", localCost)
	}
}

// Retried uploads must not double-count: the request_id is the
// idempotency key.
func TestUsageIngest_DeduplicatesOnRequestID(t *testing.T) {
	ing, ls, _ := newTestIngestor(t, []EdgeClient{{Name: "e", Token: "t"}})
	body := `{"records":[{"request_id":"same","source":"local","model":"m","status":200}]}`

	_ = postUsage(t, ing, "t", body)
	rec := postUsage(t, ing, "t", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("replay status = %d", rec.Code)
	}

	var resp struct {
		Accepted   int64 `json:"accepted"`
		Duplicates int64 `json:"duplicates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Accepted != 0 || resp.Duplicates != 1 {
		t.Fatalf("resp = %+v, want accepted=0 duplicates=1", resp)
	}

	ls.Flush()
	_, total, _ := ls.Query(logstore.QueryFilter{Limit: 10}, nil)
	if total != 1 {
		t.Fatalf("total = %d, want 1 (replay must not duplicate)", total)
	}
}

func TestUsageIngest_InputValidation(t *testing.T) {
	ing, _, _ := newTestIngestor(t, []EdgeClient{{Name: "e", Token: "t"}})

	cases := map[string]struct {
		method string
		body   string
		want   int
	}{
		"GET":            {http.MethodGet, "", http.StatusMethodNotAllowed},
		"invalid json":   {http.MethodPost, `{nope`, http.StatusBadRequest},
		"empty records":  {http.MethodPost, `{"records":[]}`, http.StatusBadRequest},
		"unknown source": {http.MethodPost, `{"records":[{"request_id":"x","source":"teleport"}]}`, http.StatusOK},
		"missing id":     {http.MethodPost, `{"records":[{"source":"proxy"}]}`, http.StatusOK},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/client/v1/usage", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer t")
			rec := httptest.NewRecorder()
			ing.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

// No configured clients means no endpoint surface at all.
func TestUsageIngest_DisabledWhenNoClients(t *testing.T) {
	ing, _, _ := newTestIngestor(t, nil)
	rec := postUsage(t, ing, "anything", `{"records":[{"request_id":"x","source":"proxy"}]}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 with no configured clients", rec.Code)
	}
}

func TestUsageIngest_PrunesDedupMap(t *testing.T) {
	ing := NewIngestor(nil, []EdgeClient{{Name: "e", Token: "t"}})
	ing.evictAfter = time.Millisecond
	for i := 0; i < 5; i++ {
		ing.alreadySeen("e", itoa(i))
	}
	time.Sleep(2 * time.Millisecond)
	// Prune runs when the map exceeds the threshold.
	ing.mu.Lock()
	ing.seen["filler"] = time.Now().Add(-time.Hour)
	for i := 0; i < 100000; i++ {
		ing.seen["k"+itoa(i)] = time.Now()
	}
	ing.mu.Unlock()
	_ = ing.alreadySeen("e", "new")
	ing.mu.Lock()
	_, fillerAlive := ing.seen["filler"]
	ing.mu.Unlock()
	if fillerAlive {
		t.Fatal("eviction did not prune stale entries")
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

var _ = model.Log{}
