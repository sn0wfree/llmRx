package clientapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/logstore"
)

// newHealthTestIngestor builds an Ingestor with one enrolled client and a
// real (temp-dir) logstore so the health/usage paths can run.
func newHealthTestIngestor(t *testing.T, clients []EdgeClient) *Ingestor {
	t.Helper()
	ls, err := logstore.New(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("logstore.New: %v", err)
	}
	t.Cleanup(func() { _ = ls.Close() })
	return NewIngestor(ls, clients)
}

// TestHandleHealth_ListsClients covers the healthz endpoint (was 0%
// covered): it reports status ok and the configured client count.
func TestHandleHealth_ListsClients(t *testing.T) {
	ing := newHealthTestIngestor(t, []EdgeClient{{Name: "edge-1", Token: "tok-1"}})
	rec := httptest.NewRecorder()
	ing.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/client/v1/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Status  string `json:"status"`
		Clients int    `json:"clients"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if body.Status != "ok" || body.Clients != 1 {
		t.Fatalf("body = %+v, want status=ok clients=1", body)
	}
}

// TestAuthenticateFull_RejectsBadHeaders: every malformed credential
// shape is rejected (missing header, wrong scheme, empty bearer,
// unknown token).
func TestAuthenticateFull_RejectsBadHeaders(t *testing.T) {
	ing := newHealthTestIngestor(t, []EdgeClient{{Name: "edge-1", Token: "good"}})
	cases := []struct {
		name, header string
	}{
		{"no header", ""},
		{"wrong scheme", "Basic abc"},
		{"empty bearer", "Bearer "},
		{"unknown token", "Bearer nope"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/client/v1/heartbeat", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			if _, _, ok := ing.authenticateFull(req); ok {
				t.Fatalf("authenticateFull accepted %q", c.header)
			}
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/client/v1/heartbeat", nil)
	req.Header.Set("Authorization", "Bearer good")
	name, tok, ok := ing.authenticateFull(req)
	if !ok || name != "edge-1" || tok != "good" {
		t.Fatalf("valid credential rejected: name=%q tok=%q ok=%v", name, tok, ok)
	}
}

// TestHandler_EmptyClients: healthz stays open (liveness must work
// even before any client is enrolled) and reports zero clients, while
// the authenticated endpoints reject everything. The deployment-level
// "mount nothing" decision happens in server.go (the whole handler is
// only mounted when the client list is non-empty) — not here.
func TestHandler_EmptyClients(t *testing.T) {
	ing := newHealthTestIngestor(t, nil)
	h := ing.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/client/v1/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200 (liveness always open)", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/client/v1/config", nil)
	req.Header.Set("Authorization", "Bearer anything")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("config with no clients = %d, want 401", rec.Code)
	}
}
