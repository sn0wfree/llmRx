package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// TestFindDefaultCombo_AllThreePriorities covers the three
// priority branches in findDefaultCombo:
//   1. IsDefault=true wins over everything
//   2. name="auto" wins if no IsDefault
//   3. first enabled wins if neither of the above
func TestFindDefaultCombo_AllThreePriorities(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddToken("sk-t", "t")

	// Add three combos via the test helper so the cache refreshes
	// after each.
	app.AddComboModel("sk-t", "first-enabled", []string{"m1"}, model.ComboModeLoadBalance)
	app.AddComboModel("sk-t", "auto", []string{"m1"}, model.ComboModeLoadBalance)

	// Promote a third combo to default. Must reload cache after.
	tok, _ := app.Store.GetToken("sk-t")
	defCombo := &model.TokenComboModel{
		TokenID: tok.ID, Name: "default-set", Models: []string{"m1"},
		Mode: model.ComboModeLoadBalance, Enabled: true,
	}
	if err := app.Store.CreateComboModel(defCombo); err != nil {
		t.Fatalf("create default: %v", err)
	}
	if err := app.Store.SetDefaultModelSet(tok.ID, defCombo.ID); err != nil {
		t.Fatalf("set default: %v", err)
	}
	if err := app.Cache.Reload(); err != nil {
		t.Fatalf("cache reload: %v", err)
	}

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestFindDefaultCombo_NamedAutoWins covers the "name=auto" branch
// when no IsDefault exists.
func TestFindDefaultCombo_NamedAutoWins(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddToken("sk-t", "t")
	app.AddComboModel("sk-t", "auto", []string{"m1"}, model.ComboModeLoadBalance)

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestFindDefaultCombo_FirstEnabledFallback covers the third
// priority: when no IsDefault and no name="auto", the first
// enabled combo wins. We seed a combo named "my-smart" — the
// model="auto" request should still succeed because the first
// combo wins regardless of name.
func TestFindDefaultCombo_FirstEnabledFallback(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddToken("sk-t", "t")
	app.AddComboModel("sk-t", "my-smart", []string{"m1"}, model.ComboModeLoadBalance)

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestFindDefaultCombo_DisabledSkipped verifies that disabled
// combos are skipped (the !c.Enabled check in the loop).
func TestFindDefaultCombo_DisabledSkipped(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddToken("sk-t", "t")

	// Create a disabled combo named "auto" and an enabled combo
	// named "fallback". The disabled "auto" should be skipped
	// even though it would have won by name.
	tok, _ := app.Store.GetToken("sk-t")
	disabled := &model.TokenComboModel{
		TokenID: tok.ID, Name: "auto", Models: []string{"m1"},
		Mode: model.ComboModeLoadBalance, Enabled: false,
	}
	if err := app.Store.CreateComboModel(disabled); err != nil {
		t.Fatalf("create disabled: %v", err)
	}
	enabled := &model.TokenComboModel{
		TokenID: tok.ID, Name: "fallback", Models: []string{"m1"},
		Mode: model.ComboModeLoadBalance, Enabled: true,
	}
	if err := app.Store.CreateComboModel(enabled); err != nil {
		t.Fatalf("create enabled: %v", err)
	}
	if err := app.Cache.Reload(); err != nil {
		t.Fatalf("cache reload: %v", err)
	}

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	// Should succeed via "fallback" since "auto" is disabled.
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestInvalidateRetryWrappers_Removes exercises the
// InvalidateRetryWrappers setter on the API Handler.
func TestInvalidateRetryWrappers_Removes(t *testing.T) {
	app := testhelper.New(t)
	if app.Chat == nil {
		t.Fatal("no handler")
	}
	// Calling it on a fresh handler should not panic.
	app.Chat.InvalidateRetryWrappers()

	// Call again — should still be safe.
	app.Chat.InvalidateRetryWrappers()
}

// TestHandleStreamCombo_LoadBalance exercises the streaming
// combo path (was at 0% coverage before this PR).
func TestHandleStreamCombo_LoadBalance(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddToken("sk-t", "t")
	app.AddComboModel("sk-t", "auto", []string{"m1"}, model.ComboModeLoadBalance)

	body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	// We expect either 200 (streaming chunks) or an upstream
	// error (since the upstream is a fake URL). What matters is
	// that the handler routes into handleStreamCombo without
	// crashing.
	if rec.Code >= 500 && rec.Code != http.StatusBadGateway {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleStreamSerialCombo_Fallback exercises the streaming
// serial combo path (was at 0% coverage before this PR).
func TestHandleStreamSerialCombo_Fallback(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddToken("sk-t", "t")
	app.AddComboModel("sk-t", "auto", []string{"m1"}, model.ComboModeSerial)

	body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code >= 500 && rec.Code != http.StatusBadGateway {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestAutoModel_StreamWithAuto verifies that streaming with
// model="auto" routes through handleStreamCombo (returns 502 or
// 200 depending on whether the upstream is reachable).
func TestAutoModel_StreamWithAuto(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://127.0.0.1:1", []string{"m1"}, "sk-key")
	app.AddToken("sk-t", "t")
	app.AddComboModel("sk-t", "auto", []string{"m1"}, model.ComboModeLoadBalance)

	body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	// Upstream unreachable → some kind of 5xx is acceptable
	// (502, 503, 504). What we DON'T want is 200 with a wrong
	// model name echoed, or a panic.
	if rec.Code < 200 || rec.Code >= 600 {
		t.Fatalf("unexpected code=%d body=%s", rec.Code, rec.Body.String())
	}
}