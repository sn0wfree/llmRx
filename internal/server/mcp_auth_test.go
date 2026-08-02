package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	authmw "github.com/sn0wfree/llmRx/internal/middleware"
)

// TestMCPEndpointAuth: /mcp/llmrx must sit behind the same token
// chain as /v1 — the channel_invoke tool spends the gateway's
// upstream keys, so an unauthenticated endpoint would be a free
// LLM proxy.
func TestMCPEndpointAuth(t *testing.T) {
	lookup := func(key string) (authmw.TokenInfo, bool) {
		if key == "sk-valid" {
			return authmw.TokenInfo{ID: 1, Key: key, ExpiresAt: time.Now().Add(time.Hour)}, true
		}
		return authmw.TokenInfo{}, false
	}

	engine := chi.NewRouter()
	engine.With(authmw.WithLimitsAndOptions(lookup, nil, nil)).
		Post("/mcp/llmrx", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"ok":true}}`))
		})

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}`
	req := func(auth string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/mcp/llmrx", strings.NewReader(body))
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		return r
	}

	// No token -> 401.
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req(""))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", rr.Code)
	}

	// Unknown token -> 403 (no BYOK hook wired).
	rr = httptest.NewRecorder()
	engine.ServeHTTP(rr, req("Bearer sk-nope"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("bad token: status = %d, want 403", rr.Code)
	}

	// Valid token -> the handler runs.
	rr = httptest.NewRecorder()
	engine.ServeHTTP(rr, req("Bearer sk-valid"))
	if rr.Code != http.StatusOK {
		t.Fatalf("valid token: status = %d, want 200", rr.Code)
	}
	var resp struct {
		Result map[string]bool `json:"result"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil || !resp.Result["ok"] {
		t.Fatalf("valid token body: %q err=%v", rr.Body.String(), err)
	}
}
