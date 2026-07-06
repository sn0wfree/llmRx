package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var br *bytes.Reader
	if body != "" {
		br = bytes.NewReader([]byte(body))
	} else {
		br = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, br)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestChat_NoAuth(t *testing.T) {
	app := testhelper.New(t)
	rec := do(t, app.Mux, http.MethodPost, "/v1/chat/completions",
		`{"model":"x","messages":[]}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestChat_BadToken(t *testing.T) {
	app := testhelper.New(t)
	rec := do(t, app.Mux, http.MethodPost, "/v1/chat/completions",
		`{"model":"x","messages":[]}`)
	_ = rec

	rec2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"x","messages":[]}`))
	r2.Header.Set("Content-Type", "application/json")
	r2.Header.Set("Authorization", "Bearer bogus")
	app.Mux.ServeHTTP(rec2, r2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d %s", rec2.Code, rec2.Body.String())
	}
}

func TestChat_MissingModel(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-key")
	app.AddToken("sk-t", "t")

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[]}`))
	r.Header.Set("Authorization", "Bearer sk-t")
	r.Header.Set("Content-Type", "application/json")
	app.Mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "missing_model") {
		t.Fatalf("expected missing_model, got %s", rec.Body.String())
	}
}

func TestChat_StreamNotSupported(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-key")
	app.AddToken("sk-t", "t")

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","stream":true,"messages":[]}`))
	r.Header.Set("Authorization", "Bearer sk-t")
	r.Header.Set("Content-Type", "application/json")
	app.Mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestChat_NoChannelForModel(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"known-model"}, "sk-key")
	app.AddToken("sk-t", "t")

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"unknown","messages":[]}`))
	r.Header.Set("Authorization", "Bearer sk-t")
	r.Header.Set("Content-Type", "application/json")
	app.Mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no_channel") {
		t.Fatalf("expected no_channel, got %s", rec.Body.String())
	}
}

func TestChat_InvalidBody(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-key")
	app.AddToken("sk-t", "t")

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader("not json"))
	r.Header.Set("Authorization", "Bearer sk-t")
	r.Header.Set("Content-Type", "application/json")
	app.Mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestChat_HappyPath(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannelWithPrice("c", "openai", "https://x", []string{"gpt-4"}, 0.14, 0.42, "sk-key")
	tok := app.AddToken("sk-t", "t")
	app.Provider.Responses = []*provider.ChatResponse{
		{
			ID: "chatcmpl-1", Object: "chat.completion", Model: "gpt-4",
			Choices: []provider.Choice{{Index: 0, Message: provider.Message{Role: "assistant", Content: "hi"}, FinishReason: "stop"}},
			Usage:   provider.Usage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10},
		},
	}
	app.Provider.Statuses = []int{200}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`))
	r.Header.Set("Authorization", "Bearer sk-t")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	app.Mux.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
	}
	var resp provider.ChatResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != "chatcmpl-1" || len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hi" {
		t.Fatalf("response: %+v", resp)
	}

	if app.Provider.LastKey != "sk-key" {
		t.Fatalf("expected mock to receive sk-key, got %q", app.Provider.LastKey)
	}
	if app.Provider.LastURL != "https://x" {
		t.Fatalf("expected baseURL https://x, got %q", app.Provider.LastURL)
	}

	logs, err := app.Store.GetLogs(10, 0)
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log row, got %d", len(logs))
	}
	got := logs[0]
	if got.TokenID != tok.ID {
		t.Errorf("log.TokenID: want %d got %d", tok.ID, got.TokenID)
	}
	if got.Model != "gpt-4" {
		t.Errorf("log.Model: want gpt-4 got %s", got.Model)
	}
	if got.PromptTokens != 7 || got.CompletionTokens != 3 {
		t.Errorf("log tokens: %+v", got)
	}
	if got.StatusCode != 200 {
		t.Errorf("log.StatusCode: want 200 got %d", got.StatusCode)
	}
	if got.RequestIP != "203.0.113.7" {
		t.Errorf("log.RequestIP: want 203.0.113.7 got %s", got.RequestIP)
	}
	if got.RealCostUSD <= 0 {
		t.Errorf("log.RealCostUSD should be > 0, got %f", got.RealCostUSD)
	}
}

func TestChat_UpstreamErrorLogged(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-key")
	app.AddToken("sk-t", "t")
	app.Provider.Errs = []error{fmt.Errorf("boom")}
	app.Provider.Statuses = []int{502}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[]}`))
	r.Header.Set("Authorization", "Bearer sk-t")
	r.Header.Set("Content-Type", "application/json")
	app.Mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d %s", rec.Code, rec.Body.String())
	}

	logs, _ := app.Store.GetLogs(10, 0)
	if len(logs) != 1 || logs[0].StatusCode != 502 {
		t.Fatalf("expected 1 fail log with 502, got %+v", logs)
	}
}

func TestChat_ListModels(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"a", "b"}, "sk-k")
	app.AddToken("sk-t", "t")

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer sk-t")
	app.Mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 models, got %d (%+v)", len(resp.Data), resp.Data)
	}
}

func TestChat_TokenContextConstants(t *testing.T) {
	// sanity: TokenKey / TokenIDKey are real constants (not zero)
	if middleware.TokenKey == "" {
		t.Fatal("TokenKey is empty")
	}
	if middleware.TokenIDKey == "" {
		t.Fatal("TokenIDKey is empty")
	}
}

func TestChat_Health(t *testing.T) {
	app := testhelper.New(t)
	rec := do(t, app.Mux, http.MethodGet, "/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}