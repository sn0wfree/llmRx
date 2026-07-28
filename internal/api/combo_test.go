package api_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
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
