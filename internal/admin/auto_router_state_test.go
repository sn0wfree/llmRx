package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// TestAdmin_AutoRouterState: the read-only state endpoint serves
// the Thompson arms and the decision counters, and /reload resets
// both.
func TestAdmin_AutoRouterState(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddToken("sk-t", "t")
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-1")
	addAdminAutoCombo(t, app, "sk-t")

	// Fire one auto request so arms + stats accumulate.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("auto request code=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = do(t, app.Admin.Routes(), http.MethodGet, "/auto-router/state", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("state code=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Arms  map[string][2]float64 `json:"arms"`
		Stats struct {
			Decisions int64 `json:"decisions"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("state body not json: %v\n%s", err, rec.Body.String())
	}
	if len(body.Arms) == 0 {
		t.Fatalf("arms empty after one request: %s", rec.Body.String())
	}
	if _, ok := body.Arms["simple:m1"]; !ok {
		t.Fatalf("simple:m1 arm missing: %s", rec.Body.String())
	}
	if body.Stats.Decisions != 1 {
		t.Fatalf("decisions = %d, want 1", body.Stats.Decisions)
	}

	// /reload resets arms and counters.
	rec = do(t, app.Admin.Routes(), http.MethodPost, "/reload", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("reload code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = do(t, app.Admin.Routes(), http.MethodGet, "/auto-router/state", sess, "")
	var after struct {
		Arms  map[string][2]float64 `json:"arms"`
		Stats struct {
			Decisions int64 `json:"decisions"`
		} `json:"stats"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &after)
	if len(after.Arms) != 0 {
		t.Fatalf("arms not reset after /reload: %s", rec.Body.String())
	}
	if after.Stats.Decisions != 0 {
		t.Fatalf("stats not reset after /reload: %s", rec.Body.String())
	}
}

func addAdminAutoCombo(t *testing.T, app *testhelper.App, tokenKey string) {
	t.Helper()
	tok, err := app.Store.GetToken(tokenKey)
	if err != nil {
		t.Fatalf("lookup token: %v", err)
	}
	combo := &model.TokenComboModel{
		TokenID: tok.ID,
		Name:    "auto",
		Mode:    model.ComboModeAuto,
		Tiers: map[string]model.TierConfig{
			"simple":   {Models: []string{"m1"}},
			"standard": {Models: []string{"m1"}},
			"complex":  {Models: []string{"m1"}},
			"agentic":  {Models: []string{"m1"}},
		},
		Fallback: []string{"m1"},
		Enabled:  true,
	}
	if err := app.Store.CreateComboModel(combo); err != nil {
		t.Fatalf("CreateComboModel(auto): %v", err)
	}
	if err := app.Cache.Reload(); err != nil {
		t.Fatalf("reload cache: %v", err)
	}
}
