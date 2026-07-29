package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
	proberpkg "github.com/sn0wfree/llmRx/internal/prober"
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
// fast-fails the auto path when the chosen channel is marked
// unhealthy. Non-auto requests are unaffected (probe is opt-in for
// the auto alias).
func TestAutoModel_ProbeUnhealthyShortCircuit(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c1", "openai", "https://x", []string{"m1"}, "sk-key")
	app.AddToken("sk-t", "t")
	app.AddComboModel("sk-t", "auto", []string{"m1"}, model.ComboModeLoadBalance)

	chs, _ := app.Store.GetChannels()
	if len(chs) == 0 {
		t.Fatal("no channel")
	}
	p := proberpkg.New(proberpkg.Config{TTL: 60 * time.Second}, app.Store, app.Pool)
	defer p.Stop()
	// Inject a stale-but-failing probe entry so Healthy() returns false.
	ch0 := chs[0]
	p.ProbeChannel(context.Background(), &ch0)
	p.RecordForTest(ch0.ID, proberpkg.Result{OK: false, Error: "synthetic", CheckedAt: time.Now()})
	app.Chat.SetProber(p)

	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for probe-unhealthy, got %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "probe_unhealthy") {
		t.Errorf("expected probe_unhealthy code, got %s", rec.Body.String())
	}
}
