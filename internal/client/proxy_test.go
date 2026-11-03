package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/provider"
)

func chatBody(model string, temp float64) []byte {
	t := temp
	return []byte(`{"model":"` + model + `","temperature":` + formatTemp(t) + `,"messages":[{"role":"user","content":"hi"}]}`)
}

func formatTemp(t float64) string {
	if t == float64(int64(t)) {
		return itoa64(int64(t))
	}
	s := formatAmountF(t)
	return s
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func formatAmountF(f float64) string {
	s := strings.TrimRight(strings.TrimRight(
		strconvFormat(f), "0"), ".")
	return s
}

func strconvFormat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func okResponse(model string, prompt, completion int) []byte {
	return []byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"` + model +
		`","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":` + itoa64(int64(prompt)) + `,"completion_tokens":` + itoa64(int64(completion)) + `,"total_tokens":` + itoa64(int64(prompt+completion)) + `}}`)
}

// newTestProxy builds a proxy pointed at a mock upstream.
func newTestProxy(t *testing.T, upstream *httptest.Server, mutate func(*Config)) *Proxy {
	t.Helper()
	cfg := &Config{
		Listen:         "127.0.0.1:0",
		Servers:        []string{upstream.URL},
		Token:          "sk-cfg-token",
		ClientName:     "edge-test",
		CacheEnabled:   true,
		CacheTTLSec:    600,
		UploadBatchSec: 60,
	}
	if mutate != nil {
		mutate(cfg)
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	return NewProxy(cfg)
}

func chatReq(body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// ---------- passthrough ----------

func TestProxy_ForwardsAndInjectsToken(t *testing.T) {
	var gotAuth, gotClient, gotXFF string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotClient = r.Header.Get("X-Llmrx-Client")
		gotXFF = r.Header.Get("X-Forwarded-For")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(okResponse("m", 10, 5))
	}))
	defer up.Close()

	p := newTestProxy(t, up, nil)
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, chatReq(chatBody("m", 0)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer sk-cfg-token" {
		t.Fatalf("Authorization = %q, want the configured token", gotAuth)
	}
	if gotClient != "edge-test" {
		t.Fatalf("X-Llmrx-Client = %q", gotClient)
	}
	if gotXFF == "" {
		t.Fatal("X-Forwarded-For missing")
	}
	// A proxied request is accounted as such.
	if p.buffer.len() != 1 {
		t.Fatalf("usage buffer = %d, want 1", p.buffer.len())
	}
	recs := p.buffer.takeAll()
	if recs[0].Source != SourceProxy {
		t.Fatalf("source = %q, want proxy", recs[0].Source)
	}
}

// An explicit Authorization from the agent must win over the configured
// token — the client never overrides a caller's credentials.
func TestProxy_ExplicitTokenWins(t *testing.T) {
	var gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write(okResponse("m", 1, 1))
	}))
	defer up.Close()

	p := newTestProxy(t, up, nil)
	req := chatReq(chatBody("m", 0))
	req.Header.Set("Authorization", "Bearer sk-agent-token")
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, req)

	if gotAuth != "Bearer sk-agent-token" {
		t.Fatalf("Authorization = %q, want the agent's token", gotAuth)
	}
}

// ---------- caching ----------

// The same deterministic request twice must hit the local cache on the
// second call.
func TestProxy_CachesDeterministicRequests(t *testing.T) {
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write(okResponse("m", 10, 5))
	}))
	defer up.Close()

	p := newTestProxy(t, up, nil)
	h := p.Handler()

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, chatReq(chatBody("m", 0)))
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: status %d", i, rec.Code)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1 (second must be a cache hit)", calls.Load())
	}

	recs := p.buffer.takeAll()
	if len(recs) != 2 {
		t.Fatalf("usage rows = %d, want 2", len(recs))
	}
	if recs[0].Source != SourceProxy || recs[1].Source != SourceCache {
		t.Fatalf("sources = %q, %q; want proxy then cache", recs[0].Source, recs[1].Source)
	}
	// The cached row must carry the same token counts the upstream
	// reported — it was billed once, and the cache hit did not consume
	// anything.
	if recs[1].PromptTokens != 10 || recs[1].CompletionTokens != 5 {
		t.Fatalf("cached row tokens = %+v", recs[1])
	}
	if recs[1].RequestID == recs[0].RequestID {
		t.Fatal("request ids must be unique (idempotency key)")
	}
}

// The server deduplicates on clientName+requestID for 24h, so ids must
// not repeat when the client restarts and its counter resets to 1.
func TestProxy_RequestIDsUniqueAcrossRestarts(t *testing.T) {
	cfg := &Config{Servers: []string{"http://127.0.0.1:1"}}
	a := NewProxy(cfg)
	b := NewProxy(cfg)
	if a.requestNonce == b.requestNonce {
		t.Fatalf("two proxies share nonce %q — ids would collide after a restart",
			a.requestNonce)
	}
	ida := fmt.Sprintf("%s-%s-%d", cfg.ClientName, a.requestNonce, a.requestSeq.Add(1))
	idb := fmt.Sprintf("%s-%s-%d", cfg.ClientName, b.requestNonce, b.requestSeq.Add(1))
	if ida == idb {
		t.Fatalf("request ids collide across restarts: %q", ida)
	}
}

// temperature > 0 responses must not be cached.
func TestProxy_DoesNotCacheNonDeterministic(t *testing.T) {
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write(okResponse("m", 1, 1))
	}))
	defer up.Close()

	p := newTestProxy(t, up, nil)
	h := p.Handler()
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, chatReq(chatBody("m", 0.7)))
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: status %d", i, rec.Code)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2 (temperature>0 is never cached)", calls.Load())
	}
}

// Streaming requests must never be buffered or cached — they go
// through the raw streaming path.
func TestProxy_StreamsStreamingRequests(t *testing.T) {
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			_, _ = w.Write([]byte("data: chunk\n\n"))
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer up.Close()

	p := newTestProxy(t, up, nil)
	body := []byte(`{"model":"m","stream":true,"messages":[]}`)
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, chatReq(body))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatal("streamed body truncated")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
	if p.cache.stats(context.Background()).Size != 0 {
		t.Fatal("streaming response must not be cached")
	}
}

// ---------- fallback ----------

func TestProxy_OfflineServesFromCache(t *testing.T) {
	var up atomic.Value
	up.Store(httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(okResponse("m", 7, 3))
	})))
	p := newTestProxy(t, up.Load().(*httptest.Server), nil)

	// Prime the cache.
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, chatReq(chatBody("m", 0)))
	if rec.Code != http.StatusOK {
		t.Fatalf("prime: %d", rec.Code)
	}

	// Kill the server.
	up.Load().(*httptest.Server).Close()

	// First offline request fails over to the cache.
	rec2 := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec2, chatReq(chatBody("m", 0)))
	if rec2.Code != http.StatusOK {
		t.Fatalf("offline: status %d body %s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), `"content":"hello"`) {
		t.Fatalf("offline body = %s", rec2.Body.String())
	}
}

// With no cached response and a dead server, the client returns a
// structured 503 the agent can act on.
func TestProxy_OfflineNoCache503(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	p := newTestProxy(t, up, nil)
	up.Close()

	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, chatReq(chatBody("m", 0)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "client_upstream_unreachable") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

// Business errors from a live server must pass through untouched: the
// agent must see the 401, not a fallback mask.
func TestProxy_PassesThroughBusinessErrors(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid token","code":"invalid_token"}}`))
	}))
	defer up.Close()

	p := newTestProxy(t, up, nil)
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, chatReq(chatBody("m", 0)))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 passthrough", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_token") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// And a business error must NOT trip the reachability breaker.
	if open, _ := p.brk.state(); open {
		t.Fatal("401 must not open the server breaker")
	}
}

// 5xx trips the breaker so subsequent requests skip the dead server.
func TestProxy_BreakerTripsOn5xx(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer up.Close()

	p := newTestProxy(t, up, nil)
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		p.Handler().ServeHTTP(rec, chatReq(chatBody("m", 0)))
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("call %d: status %d", i, rec.Code)
		}
	}
	if open, fails := p.brk.state(); !open || fails < 3 {
		t.Fatalf("breaker not open: open=%v fails=%d", open, fails)
	}
}

// ---------- failover ----------

func TestProxy_FailsOverToSecondServer(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	alive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(okResponse("m", 1, 1))
	}))
	defer alive.Close()
	dead.Close() // port closed but URL shape valid

	p := newTestProxy(t, alive, func(c *Config) {
		c.Servers = []string{dead.URL, alive.URL}
	})

	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, chatReq(chatBody("m", 0)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

// ---------- breaker unit ----------

func TestBreaker_States(t *testing.T) {
	b := newBreaker(2, time.Second)
	if !b.allow(time.Now()) {
		t.Fatal("closed breaker must allow")
	}
	b.failure()
	b.failure()
	if open, _ := b.state(); !open {
		t.Fatal("breaker must open at threshold")
	}
	if b.allow(time.Now()) {
		t.Fatal("open breaker must block within cooldown")
	}
	if !b.allow(time.Now().Add(2 * time.Second)) {
		t.Fatal("open breaker must half-open after cooldown")
	}
	b.success()
	if open, _ := b.state(); open {
		t.Fatal("success must close the breaker")
	}
}

func TestFallible(t *testing.T) {
	cases := []struct {
		name   string
		status int
		err    error
		want   bool
	}{
		{"transport error", 0, context.Canceled, true},
		{"408", httpStatusRequestTimeout, nil, true},
		{"500", 500, nil, true},
		{"401", 401, nil, false},
		{"429", 429, nil, false},
		{"200", 200, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fallible(tc.status, tc.err); got != tc.want {
				t.Fatalf("fallible = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------- config ----------

func TestConfig_Normalize(t *testing.T) {
	t.Run("missing servers", func(t *testing.T) {
		c := &Config{}
		if err := c.Normalize(); err == nil || !strings.Contains(err.Error(), "servers") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bad scheme", func(t *testing.T) {
		c := &Config{Servers: []string{"api.example.com"}}
		if err := c.Normalize(); err == nil || !strings.Contains(err.Error(), "http") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("defaults applied", func(t *testing.T) {
		c := &Config{Servers: []string{"http://x"}}
		if err := c.Normalize(); err != nil {
			t.Fatalf("Normalize: %v", err)
		}
		if c.Listen != "127.0.0.1:3425" {
			t.Fatalf("Listen = %q", c.Listen)
		}
		if c.UploadBatchSec != 60 || c.CacheTTLSec != 3600 {
			t.Fatalf("defaults not applied: %+v", c)
		}
	})
	t.Run("usage url derived", func(t *testing.T) {
		c := &Config{Servers: []string{"https://s.example/"}}
		_ = c.Normalize()
		if got := c.usageURL(); got != "https://s.example/client/v1/usage" {
			t.Fatalf("usageURL = %q", got)
		}
	})
}

// ---------- usage persistence ----------

func TestJSONLUsageStore_RoundTrip(t *testing.T) {
	path := t.TempDir() + "/usage.jsonl"
	s, err := NewJSONLUsageStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	recs := []UsageRecord{
		{RequestID: "a", Source: SourceProxy, Model: "m", Status: 200},
		{RequestID: "b", Source: SourceCache, Model: "m", Status: 200},
	}
	if err := s.Append(recs); err != nil {
		t.Fatalf("Append: %v", err)
	}
	_ = s.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Count(string(data), "\n") != 2 {
		t.Fatalf("file = %q, want 2 lines", string(data))
	}
	var back UsageRecord
	line := strings.SplitN(string(data), "\n", 2)[0]
	if err := json.Unmarshal([]byte(line), &back); err != nil {
		t.Fatalf("line not JSON: %v", err)
	}
	if back.RequestID != "a" || back.Source != SourceProxy {
		t.Fatalf("record = %+v", back)
	}
}

func TestUsageBuffer_EvictsOldest(t *testing.T) {
	u := newUsageBuffer(3)
	for i := 0; i < 5; i++ {
		u.add(UsageRecord{RequestID: itoa64(int64(i))})
	}
	if u.len() != 3 {
		t.Fatalf("len = %d, want 3", u.len())
	}
	got := u.takeAll()
	if got[0].RequestID != "2" {
		t.Fatalf("oldest not evicted: first = %q, want \"2\"", got[0].RequestID)
	}
}

// ---------- usage upload ----------

func TestDrainUsage_UploadsAndClears(t *testing.T) {
	var gotToken string
	var gotBody map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	p := newTestProxy(t, up, func(c *Config) {
		c.ClientToken = "sk-edge"
		c.ClientName = "edge-1"
		c.UsageEndpoint = up.URL + "/client/v1/usage"
	})
	p.buffer.add(UsageRecord{RequestID: "r1", Source: SourceProxy})

	n, err := p.DrainUsage(context.Background(), up.Client())
	if err != nil {
		t.Fatalf("DrainUsage: %v", err)
	}
	if n != 1 {
		t.Fatalf("uploaded = %d, want 1", n)
	}
	if gotToken != "Bearer sk-edge" {
		t.Fatalf("Authorization = %q", gotToken)
	}
	if gotBody["client_name"] != "edge-1" {
		t.Fatalf("body = %v", gotBody)
	}
	if p.buffer.len() != 0 {
		t.Fatalf("buffer = %d, want 0", p.buffer.len())
	}
}

// A failed upload must requeue the batch, not lose it.
func TestDrainUsage_RequeuesOnFailure(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer up.Close()

	p := newTestProxy(t, up, func(c *Config) {
		c.ClientToken = "sk-edge"
		c.ClientName = "edge-1"
		c.UsageEndpoint = up.URL + "/client/v1/usage"
	})
	p.buffer.add(UsageRecord{RequestID: "r1"})
	p.buffer.add(UsageRecord{RequestID: "r2"})

	if _, err := p.DrainUsage(context.Background(), up.Client()); err == nil {
		t.Fatal("expected upload failure")
	}
	if p.buffer.len() != 2 {
		t.Fatalf("buffer = %d, want 2 (rows must survive a failed upload)", p.buffer.len())
	}
	// Order preserved: r1 stays first.
	got := p.buffer.takeAll()
	if got[0].RequestID != "r1" || got[1].RequestID != "r2" {
		t.Fatalf("requeue order wrong: %q, %q", got[0].RequestID, got[1].RequestID)
	}
}

// No client token configured → uploads are a no-op, not an error.
func TestDrainUsage_Disabled(t *testing.T) {
	p := newTestProxy(t, httptest.NewServer(nil), nil)
	p.buffer.add(UsageRecord{RequestID: "r1"})
	n, err := p.DrainUsage(context.Background(), http.DefaultClient)
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v, want 0,nil", n, err)
	}
}

var _ = provider.ChatRequest{}
