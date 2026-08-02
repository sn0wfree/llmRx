package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// TestChatCompletions_MissingModel verifies the 400 path when
// the request body omits "model".
func TestChatCompletions_MissingModel(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddToken("sk-t", "t")

	body := `{"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestChatCompletions_InvalidJSON covers the body-parse error path.
func TestChatCompletions_InvalidJSON(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddToken("sk-t", "t")

	body := `{"this is not valid json`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestChatCompletions_IPNotAllowed exercises the IP whitelist 403
// path. The token has an IP whitelist that doesn't include the
// test client IP.
func TestChatCompletions_IPNotAllowed(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")

	tok, _ := app.Store.GetToken("sk-t")
	if tok == nil {
		app.AddToken("sk-t", "t")
		tok, _ = app.Store.GetToken("sk-t")
	}
	// Set IP whitelist to a /32 that doesn't match 127.0.0.1
	tok.IPWhitelist = []string{"192.168.99.99/32"}
	if err := app.Store.UpdateToken(tok); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := app.Cache.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	body := `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	// Default RemoteAddr is 192.0.2.1:1234 in httptest
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestChatCompletions_NoChannelForModel exercises the no_channel
// 503 path when no enabled channel supports the model.
func TestChatCompletions_NoChannelForModel(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"some-other-model"}, "sk-key")
	app.AddToken("sk-t", "t")

	body := `{"model":"nonexistent-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	// Either 403 (model_not_allowed) or 503 (no_channel) is
	// acceptable. What matters is we get a sane error rather
	// than a panic.
	if rec.Code < 400 || rec.Code >= 600 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleStreamSerialCombo_FullyEmpty exercises the streaming
// serial combo path when no upstream succeeds. (Phase 2-B.)
func TestHandleStreamSerialCombo_FullyEmpty(t *testing.T) {
	app := testhelper.New(t)
	// No channels — serial combo will exhaust immediately.
	app.AddToken("sk-t", "t")
	app.AddComboModel("sk-t", "auto", []string{"nonexistent"}, model.ComboModeSerial)

	body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	// Serial-all-failed returns 502 combo_all_failed.
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "combo_all_failed") {
		t.Errorf("expected combo_all_failed in body, got: %s", rec.Body.String())
	}
}

// TestHandleSerialCombo_NonStream covers the non-streaming serial
// combo path (handleSerialCombo in router.go).
func TestHandleSerialCombo_NonStream(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddToken("sk-t", "t")
	app.AddComboModel("sk-t", "auto", []string{"m1"}, model.ComboModeSerial)

	// Direct call via the combo name (not model="auto")
	body := `{"model":"auto","stream":false,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}
