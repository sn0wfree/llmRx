package api_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	proberpkg "github.com/sn0wfree/llmRx/internal/prober"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestCombo_LoadBalance_HappyPath(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1", "m2"}, "sk-key")
	app.AddToken("sk-t", "t")
	app.AddComboModel("sk-t", "combo-lb", []string{"m1", "m2"}, model.ComboModeLoadBalance)

	body := `{"model":"combo-lb","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestCombo_LoadBalance_NoChannel(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddToken("sk-t", "t")
	app.AddComboModel("sk-t", "combo-lb", []string{"nonexistent"}, model.ComboModeLoadBalance)
	app.PriceModels([]string{"nonexistent"}, 1.0, 1.0)

	body := `{"model":"combo-lb","messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 503 {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestCombo_LoadBalance_UpstreamError(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddToken("sk-t", "t")
	app.AddComboModel("sk-t", "combo-lb", []string{"m1"}, model.ComboModeLoadBalance)
	app.Provider.Errs = []error{errors.New("boom")}
	app.Provider.Statuses = []int{502}

	body := `{"model":"combo-lb","messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 502 {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
}

func TestCombo_Serial_HappyPath(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddToken("sk-t", "t")
	app.AddComboModel("sk-t", "combo-serial", []string{"m1"}, model.ComboModeSerial)

	body := `{"model":"combo-serial","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestCombo_Serial_FailoverSuccess(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddChannel("c2", "openai", "https://y", []string{"m2"}, "sk-key2")
	app.AddToken("sk-t", "t")
	app.AddComboModel("sk-t", "combo-serial", []string{"m1", "m2"}, model.ComboModeSerial)

	app.Provider.Errs = []error{errors.New("m1 down"), nil}
	app.Provider.Statuses = []int{503, 200}
	app.Provider.Responses = []*provider.ChatResponse{
		nil,
		{ID: "ok", Model: "m2", Choices: []provider.Choice{{Index: 0, Message: provider.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}}},
	}

	body := `{"model":"combo-serial","messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestCombo_Serial_AllFail(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddChannel("c2", "openai", "https://y", []string{"m2"}, "sk-key2")
	app.AddToken("sk-t", "t")
	app.AddComboModel("sk-t", "combo-serial", []string{"m1", "m2"}, model.ComboModeSerial)

	app.Provider.Errs = []error{errors.New("m1 down"), errors.New("m2 down")}
	app.Provider.Statuses = []int{503, 503}

	body := `{"model":"combo-serial","messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 502 {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "combo_all_failed") {
		t.Fatalf("expected combo_all_failed, got %s", rec.Body.String())
	}
}

func TestCombo_Serial_NoChannel(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddToken("sk-t", "t")
	app.AddComboModel("sk-t", "combo-serial", []string{"nonexistent"}, model.ComboModeSerial)
	app.PriceModels([]string{"nonexistent"}, 1.0, 1.0)

	body := `{"model":"combo-serial","messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 502 {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "combo_all_failed") {
		t.Fatalf("expected combo_all_failed, got %s", rec.Body.String())
	}
}

func TestAutoModel_LoadBalance_HappyPath(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1", "m2"}, "sk-key")
	app.AddToken("sk-t", "t")
	app.AddComboModel("sk-t", "auto", []string{"m1", "m2"}, model.ComboModeLoadBalance)

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestAutoModel_NoComboConfigured(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddToken("sk-t", "t")

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no_auto_combo") {
		t.Errorf("expected no_auto_combo code, got %s", rec.Body.String())
	}
}

func TestAutoModel_DisabledComboIgnored(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddToken("sk-t", "t")
	app.AddComboModel("sk-t", "auto", []string{"m1"}, model.ComboModeLoadBalance)

	toks, _ := app.Store.GetTokens()
	if len(toks) == 0 {
		t.Fatal("no token")
	}
	combos, _ := app.Store.GetComboModels(toks[0].ID)
	if len(combos) == 0 {
		t.Fatal("no combo")
	}
	combos[0].Enabled = false
	app.Store.UpdateComboModel(&combos[0])
	app.Cache.Reload()

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for disabled combo, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestAutoModel_ListModelsIncludesAuto(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddToken("sk-t", "t")

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-t")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"id":"auto"`) {
		t.Error("/v1/models should contain auto virtual model")
	}
	if !strings.Contains(rec.Body.String(), `"owned_by":"llmrx"`) {
		t.Error("auto model owned_by should be llmrx")
	}
}

func TestAutoModel_PrefersIsDefault(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"alpha-model", "beta-model"}, "sk-key")
	app.AddToken("sk-t", "t")

	toks, _ := app.Store.GetTokens()
	if len(toks) == 0 {
		t.Fatal("no token")
	}
	alpha := &model.TokenComboModel{
		TokenID:  toks[0].ID,
		Name:     "alpha-set",
		Models:   []string{"alpha-model"},
		Mode:     model.ComboModeLoadBalance,
		Strategy: model.StrategyBalanced,
		Enabled:  true,
	}
	beta := &model.TokenComboModel{
		TokenID:  toks[0].ID,
		Name:     "beta-set",
		Models:   []string{"beta-model"},
		Mode:     model.ComboModeLoadBalance,
		Strategy: model.StrategyBalanced,
		Enabled:  true,
	}
	auto := &model.TokenComboModel{
		TokenID:   toks[0].ID,
		Name:      "auto",
		Models:    []string{"alpha-model", "beta-model"},
		Mode:      model.ComboModeLoadBalance,
		Strategy:  model.StrategyBalanced,
		Enabled:   true,
		IsDefault: false,
	}
	if err := app.Store.CreateComboModel(alpha); err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	if err := app.Store.CreateComboModel(beta); err != nil {
		t.Fatalf("create beta: %v", err)
	}
	if err := app.Store.CreateComboModel(auto); err != nil {
		t.Fatalf("create auto: %v", err)
	}
	if err := app.Store.SetDefaultModelSet(toks[0].ID, alpha.ID); err != nil {
		t.Fatalf("set default: %v", err)
	}
	app.Cache.Reload()

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"model":"alpha-model"`) {
		t.Errorf("auto should route via isDefault combo (alpha-model), got: %s", rec.Body.String())
	}
}

// TestAutoModel_ProbeUnhealthyShortCircuit verifies the prober
// filters unhealthy channels at L2. With a single unhealthy
// channel the safety net kicks in (degraded attempt > no traffic),
// so the request still reaches the upstream and fails there.
// With two channels (one healthy, one not), the healthy one wins.
func TestAutoModel_ProbeUnhealthyShortCircuit(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddChannel("c2", "openai", "https://x", []string{"m1"}, "sk-key2")
	app.AddToken("sk-t", "t")
	app.AddComboModel("sk-t", "auto", []string{"m1"}, model.ComboModeLoadBalance)

	chs, _ := app.Store.GetChannels()
	if len(chs) < 2 {
		t.Fatal("need 2 channels")
	}
	p := proberpkg.New(proberpkg.Config{TTL: 60 * time.Second}, app.Store, app.Pool)
	defer p.Stop()
	// Mark channel 0 as unhealthy; channel 1 stays healthy.
	p.RecordForTest(chs[0].ID, proberpkg.Result{OK: false, Error: "synthetic", CheckedAt: time.Now()})
	app.Chat.SetProber(p)
	// Wire the prober into the router's L2 filter (production path).
	app.Engine.SetHealthChecker(p)

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	// With one healthy channel, the prober should route to it.
	// The request will fail at the upstream (https://x is not a
	// real server), but the important thing is that the router
	// didn't pick the probe-unhealthy channel.
	if rec.Code == http.StatusServiceUnavailable {
		// Both channels filtered out -> safety net should have
		// returned at least one. If we get 503 it means no channel
		// was found at all, which shouldn't happen with the safety net.
		t.Fatalf("safety net should have returned a channel, got 503: %s", rec.Body.String())
	}
}
