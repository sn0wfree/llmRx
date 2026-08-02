package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// addAutoCombo creates a mode:auto combo on the token with the
// given tier table and fallback list.
func addAutoCombo(t *testing.T, app *testhelper.App, tokenKey string, tiers map[string]model.TierConfig, fallback []string) {
	t.Helper()
	tok, err := app.Store.GetToken(tokenKey)
	if err != nil {
		t.Fatalf("lookup token: %v", err)
	}
	combo := &model.TokenComboModel{
		TokenID:  tok.ID,
		Name:     "auto",
		Mode:     model.ComboModeAuto,
		Tiers:    tiers,
		Fallback: fallback,
		Enabled:  true,
	}
	if err := app.Store.CreateComboModel(combo); err != nil {
		t.Fatalf("CreateComboModel(auto): %v", err)
	}
	if err := app.Cache.Reload(); err != nil {
		t.Fatalf("reload cache: %v", err)
	}
}

func autoComboTiers() map[string]model.TierConfig {
	return map[string]model.TierConfig{
		"simple":   {Models: []string{"m1", "m2"}},
		"standard": {Models: []string{"m2"}},
		"complex":  {Models: []string{"m2"}},
		"agentic":  {Models: []string{"m2"}},
	}
}

func doAutoChat(t *testing.T, app *testhelper.App, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)
	return rec
}

// TestAutoCombo_SimpleTierColdStart: a greeting lands in the
// simple tier, the cold-start gate picks the cheapest candidate,
// and the decision is stamped on the response headers.
func TestAutoCombo_SimpleTierColdStart(t *testing.T) {
	app := testhelper.New(t)
	app.AddToken("sk-t", "t")
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-1")
	app.AddChannel("c2", "openai", "https://x", []string{"m2"}, "sk-2")
	addAutoCombo(t, app, "sk-t", autoComboTiers(), []string{"m2"})

	rec := doAutoChat(t, app, `{"model":"auto","messages":[{"role":"user","content":"hello"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-llmRx-Auto-Tier"); got != "simple" {
		t.Errorf("tier header = %q, want simple", got)
	}
	if got := rec.Header().Get("X-llmRx-Routed-Model"); got != "m1" {
		t.Errorf("routed header = %q, want m1 (cold start = cheapest)", got)
	}
	// The successful call must update the (simple, m1) quality arm:
	// prior (1,1) + one success = (2,1).
	arms := app.Engine.Thompson().SnapshotArms()
	if arms["simple:m1"] != [2]float64{2, 1} {
		t.Errorf("simple:m1 arm = %v, want [2 1]", arms["simple:m1"])
	}
}

// TestAutoCombo_Failover: the first tier candidate fails with a
// 5xx, the next one succeeds; both channel and arm posters update
// for each attempt.
func TestAutoCombo_Failover(t *testing.T) {
	app := testhelper.New(t)
	app.AddToken("sk-t", "t")
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-1")
	app.AddChannel("c2", "openai", "https://x", []string{"m2"}, "sk-2")
	addAutoCombo(t, app, "sk-t", autoComboTiers(), []string{"m2"})

	app.Provider.Statuses = []int{500}
	app.Provider.Errs = []error{errors.New("upstream boom")}

	rec := doAutoChat(t, app, `{"model":"auto","messages":[{"role":"user","content":"hello"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-llmRx-Routed-Model"); got != "m2" {
		t.Errorf("routed header = %q, want m2 after failover", got)
	}
	arms := app.Engine.Thompson().SnapshotArms()
	if arms["simple:m1"] != [2]float64{1, 2} {
		t.Errorf("simple:m1 arm = %v, want [1 2] (one failure)", arms["simple:m1"])
	}
	if arms["simple:m2"] != [2]float64{2, 1} {
		t.Errorf("simple:m2 arm = %v, want [2 1] (one success)", arms["simple:m2"])
	}
}

// TestAutoCombo_FallbackWhenTierMissing: when the classifier
// returns a tier the combo doesn't define, the fallback list takes
// over and the decision marks fallback=true.
func TestAutoCombo_FallbackWhenTierMissing(t *testing.T) {
	app := testhelper.New(t)
	app.AddToken("sk-t", "t")
	app.AddChannel("c3", "openai", "https://x", []string{"m3"}, "sk-3")
	// Only the "standard" tier exists; a greeting classifies as
	// "simple", so every attempt comes from the fallback list.
	tiers := map[string]model.TierConfig{"standard": {Models: []string{"m3"}}}
	addAutoCombo(t, app, "sk-t", tiers, []string{"m3"})

	rec := doAutoChat(t, app, `{"model":"auto","messages":[{"role":"user","content":"hello"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-llmRx-Auto-Tier"); got != "simple" {
		t.Errorf("tier header = %q, want simple", got)
	}
	if got := rec.Header().Get("X-llmRx-Routed-Model"); got != "m3" {
		t.Errorf("routed header = %q, want m3 (fallback)", got)
	}
	// Fallback models are not tier arms: no arm state may exist.
	if arms := app.Engine.Thompson().SnapshotArms(); len(arms) != 0 {
		t.Errorf("fallback-only traffic must not create arms: %v", arms)
	}
}

// TestAutoCombo_ComplexTier: a long technical prompt escalates to
// the complex (or agentic) tier.
func TestAutoCombo_ComplexTier(t *testing.T) {
	app := testhelper.New(t)
	app.AddToken("sk-t", "t")
	app.AddChannel("c2", "openai", "https://x", []string{"m2"}, "sk-2")
	addAutoCombo(t, app, "sk-t", autoComboTiers(), []string{"m2"})

	prompt := "```\n" + strings.Repeat("implement a websocket endpoint with authentication, error handling and backpressure; use goroutines and mutexes; then write unit tests for the edge cases and explain the trade-offs in the README. ", 25) + "```"
	body, err := json.Marshal(map[string]any{
		"model": "auto",
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := doAutoChat(t, app, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	tier := rec.Header().Get("X-llmRx-Auto-Tier")
	if tier != "complex" && tier != "agentic" {
		t.Errorf("tier header = %q, want complex or agentic", tier)
	}
	if got := rec.Header().Get("X-llmRx-Routed-Model"); got != "m2" {
		t.Errorf("routed header = %q, want m2", got)
	}
}

// TestAutoCombo_AllFail: when every candidate and fallback fails
// the request returns 502 but still carries the tier header.
func TestAutoCombo_AllFail(t *testing.T) {
	app := testhelper.New(t)
	app.AddToken("sk-t", "t")
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-1")
	app.AddChannel("c2", "openai", "https://x", []string{"m2"}, "sk-2")
	addAutoCombo(t, app, "sk-t", autoComboTiers(), []string{"m2"})

	app.Provider.Statuses = []int{500, 500, 500}
	app.Provider.Errs = []error{errors.New("boom"), errors.New("boom"), errors.New("boom")}

	rec := doAutoChat(t, app, `{"model":"auto","messages":[{"role":"user","content":"hello"}]}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "combo_all_failed") {
		t.Errorf("expected combo_all_failed, got %s", rec.Body.String())
	}
	if got := rec.Header().Get("X-llmRx-Auto-Tier"); got != "simple" {
		t.Errorf("tier header = %q, want simple", got)
	}
	if got := rec.Header().Get("X-llmRx-Routed-Model"); got != "" {
		t.Errorf("routed header = %q, want empty", got)
	}
	arms := app.Engine.Thompson().SnapshotArms()
	if arms["simple:m1"] != [2]float64{1, 2} || arms["simple:m2"] != [2]float64{1, 2} {
		t.Errorf("both tier arms should carry one failure: %v", arms)
	}
}

// TestAutoCombo_QualityWinsOverCost: once arms carry enough
// observations, the high-quality model is routed even though it is
// more expensive — the learning loop overrides static cost order.
func TestAutoCombo_QualityWinsOverCost(t *testing.T) {
	app := testhelper.New(t)
	app.AddToken("sk-t", "t")
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-1")
	app.AddChannel("c2", "openai", "https://x", []string{"m2"}, "sk-2")
	addAutoCombo(t, app, "sk-t", autoComboTiers(), []string{"m2"})

	for i := 0; i < 10; i++ {
		app.Engine.RecordArmSuccess("simple:m2")
		app.Engine.RecordArmFailure("simple:m1")
	}

	rec := doAutoChat(t, app, `{"model":"auto","messages":[{"role":"user","content":"hello"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-llmRx-Routed-Model"); got != "m2" {
		t.Errorf("routed header = %q, want m2 (quality beats cost order)", got)
	}
}

// TestAutoCombo_Streaming: the streaming path classifies, samples
// and streams from the first available candidate.
func TestAutoCombo_Streaming(t *testing.T) {
	app := testhelper.New(t)
	app.AddToken("sk-t", "t")
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-1")
	addAutoCombo(t, app, "sk-t", autoComboTiers(), []string{"m1"})

	app.Provider.StreamChunks = []provider.StreamChunk{
		{ID: "chunk-1", Model: "m1", Choices: []provider.StreamChoice{
			{Index: 0, Delta: provider.Message{Role: "assistant", Content: "hi"}},
		}},
	}

	rec := doAutoChat(t, app, `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-llmRx-Auto-Tier"); got != "simple" {
		t.Errorf("tier header = %q, want simple", got)
	}
	if got := rec.Header().Get("X-llmRx-Routed-Model"); got != "m1" {
		t.Errorf("routed header = %q, want m1", got)
	}
	if !strings.Contains(rec.Body.String(), "chunk-1") {
		t.Errorf("stream body should contain chunk-1: %s", rec.Body.String())
	}
}

// TestAutoCombo_FallbackBypassesBreaker: when every tier candidate
// is unroutable and the fallback channel's breaker is open, the
// safety net still attempts the fallback (SkipBreaker) — a degraded
// call beats a hard 502.
func TestAutoCombo_FallbackBypassesBreaker(t *testing.T) {
	app := testhelper.New(t)
	app.AddToken("sk-t", "t")
	// c1 serves m1 (fallback-only); c2 serves m2 (the only tier
	// candidate). m1 must NOT be a tier candidate, otherwise the
	// fallback dedup would skip it.
	c1 := app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-1")
	c2 := app.AddChannel("c2", "openai", "https://x", []string{"m2"}, "sk-2")
	tiers := map[string]model.TierConfig{
		"simple":   {Models: []string{"m2"}},
		"standard": {Models: []string{"m2"}},
		"complex":  {Models: []string{"m2"}},
		"agentic":  {Models: []string{"m2"}},
	}
	addAutoCombo(t, app, "sk-t", tiers, []string{"m1"})

	// Make the tier candidate unroutable: delete c2 from the store
	// and refresh the static router snapshot so RouteWith("m2")
	// fails, forcing the fallback path.
	if err := app.Store.DeleteChannel(c2.ID); err != nil {
		t.Fatalf("delete c2: %v", err)
	}
	app.Engine.ReloadAllChannels()

	// Open c1's breaker (5 consecutive failures).
	for i := 0; i < 5; i++ {
		app.Engine.RecordFailure(c1.ID, 500)
	}

	rec := doAutoChat(t, app, `{"model":"auto","messages":[{"role":"user","content":"hello"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s (fallback must bypass the breaker)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-llmRx-Routed-Model"); got != "m1" {
		t.Errorf("routed header = %q, want m1 via bypassed fallback", got)
	}
}

// TestAutoCombo_ContextBudget: a prompt that cannot fit the
// candidate models' context windows is routed to the model with
// headroom, even when it sits last in the tier's cost order.
func TestAutoCombo_ContextBudget(t *testing.T) {
	app := testhelper.New(t)
	app.AddToken("sk-t", "t")
	// One channel serving all three candidates.
	app.AddChannel("c1", "openai", "https://x", []string{"gpt-4o", "deepseek-chat", "claude-3-5-sonnet"}, "sk-1")
	tiers := map[string]model.TierConfig{
		"simple":   {Models: []string{"gpt-4o"}},
		"standard": {Models: []string{"gpt-4o", "deepseek-chat", "claude-3-5-sonnet"}},
		"complex":  {Models: []string{"claude-3-5-sonnet"}},
		"agentic":  {Models: []string{"claude-3-5-sonnet"}},
	}
	addAutoCombo(t, app, "sk-t", tiers, []string{"claude-3-5-sonnet"})

	// Small prompt: all candidates fit, cold start picks gpt-4o.
	rec := doAutoChat(t, app, `{"model":"auto","messages":[{"role":"user","content":"hello"}]}`)
	if got := rec.Header().Get("X-llmRx-Routed-Model"); got != "gpt-4o" {
		t.Errorf("small prompt routed = %q, want gpt-4o", got)
	}

	// 600k chars of filler ~= 150k tokens -> need 180k; gpt-4o
	// (128k) and deepseek-chat (64k) are filtered, leaving
	// claude-3-5-sonnet (unknown window, kept leniently).
	filler := strings.Repeat("aaa ", 200000)
	body, err := json.Marshal(map[string]any{
		"model": "auto",
		"messages": []map[string]string{
			{"role": "user", "content": filler},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec = doAutoChat(t, app, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-llmRx-Routed-Model"); got != "claude-3-5-sonnet" {
		t.Errorf("huge prompt routed = %q, want claude-3-5-sonnet (context budget)", got)
	}
}
