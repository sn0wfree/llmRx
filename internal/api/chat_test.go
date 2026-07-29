package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/logstore"
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
	// Replace every protocol's provider with a plainProvider that
	// does NOT implement StreamingProvider. The chat handler must
	// return 501 in that case.
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-key")
	app.AddToken("sk-t", "t")
	app.Chat.SetProviders(map[string]provider.Provider{
		"":          plainProvider{},
		"openai":    plainProvider{},
		"anthropic": plainProvider{},
		"gemini":    plainProvider{},
	})

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

// plainProvider is a non-streaming Provider used to exercise the
// stream_unsupported error path.
type plainProvider struct{}

func (plainProvider) Name() string { return "plain" }
func (plainProvider) Chat(_ context.Context, req *provider.ChatRequest, _, _ string) (*provider.ChatResponse, int, error) {
	return &provider.ChatResponse{ID: "x", Model: req.Model, Usage: provider.Usage{}}, 200, nil
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
	// Enable proxy-header trust so the XFF set on the test
	// request is honoured. testhelper's default config has
	// TrustProxyHeaders=false (the safe default for direct
	// deployments).
	app.Cfg.Server.TrustProxyHeaders = true
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

	logs, _, err := app.LogStore.Query(logstore.QueryFilter{Limit: 10}, nil)
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

	logs, _, _ := app.LogStore.Query(logstore.QueryFilter{Limit: 10, Offset: 0}, nil)
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
			ID       string `json:"id"`
			OwnedBy  string `json:"owned_by"`
			ContextW *int   `json:"context_window"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 3 {
		t.Fatalf("expected 3 models (2 real + auto), got %d (%+v)", len(resp.Data), resp.Data)
	}
	if resp.Data[0].ContextW != nil {
		t.Error("default response should NOT include context_window")
	}
	hasAuto := false
	for _, m := range resp.Data {
		if m.ID == "auto" {
			hasAuto = true
			if m.OwnedBy != "llmrx" {
				t.Errorf("auto owned_by: got %q, want llmrx", m.OwnedBy)
			}
		}
	}
	if !hasAuto {
		t.Error("response should contain 'auto' virtual model")
	}
}

func TestChat_ListModelsDetails(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"gpt-4o"}, "sk-k")
	app.AddToken("sk-t", "t")

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/models?details=true", nil)
	r.Header.Set("Authorization", "Bearer sk-t")
	app.Mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []struct {
			ID            string   `json:"id"`
			OwnedBy       string   `json:"owned_by"`
			ContextWindow *int     `json:"context_window"`
			MaxOutput     *int     `json:"max_output"`
			Pricing       *struct {
				Input  float64 `json:"input"`
				Output float64 `json:"output"`
			} `json:"pricing"`
			Capabilities *struct {
				ToolCall bool `json:"tool_call"`
			} `json:"capabilities"`
			Modalities []string `json:"modalities"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 models (1 real + auto), got %d", len(resp.Data))
	}
	var gpt4oIdx = -1
	for i := range resp.Data {
		if resp.Data[i].ID == "gpt-4o" {
			gpt4oIdx = i
			break
		}
	}
	if gpt4oIdx < 0 {
		t.Fatal("gpt-4o not found in response")
	}
	m := &resp.Data[gpt4oIdx]
	if m.ContextWindow == nil {
		t.Fatal("details=true should include context_window")
	}
	if *m.ContextWindow != 128000 {
		t.Errorf("context_window: got %d, want 128000", *m.ContextWindow)
	}
	if m.Pricing == nil {
		t.Fatal("details=true should include pricing")
	}
	if m.Pricing.Input <= 0 {
		t.Errorf("pricing.input should be positive, got %f", m.Pricing.Input)
	}
	if m.Capabilities == nil {
		t.Fatal("details=true should include capabilities")
	}
	if !m.Capabilities.ToolCall {
		t.Error("gpt-4o should have tool_call=true")
	}
	if len(m.Modalities) < 1 {
		t.Error("should have at least text modality")
	}
}

func TestChat_ListModelsDetails_UnknownModel(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"made-up-model-xyz"}, "sk-k")
	app.AddToken("sk-t", "t")

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/models?details=true", nil)
	r.Header.Set("Authorization", "Bearer sk-t")
	app.Mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []struct {
			ID            string `json:"id"`
			ContextWindow *int   `json:"context_window"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 models (1 real + auto), got %d", len(resp.Data))
	}
	var unknownIdx = -1
	for i := range resp.Data {
		if resp.Data[i].ID == "made-up-model-xyz" {
			unknownIdx = i
			break
		}
	}
	if unknownIdx < 0 {
		t.Fatal("unknown model not found in response")
	}
	if resp.Data[unknownIdx].ContextWindow != nil {
		t.Error("unknown model should NOT have context_window in details mode")
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
func TestChat_StreamingEndpoint(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-aaaa")
	app.AddToken("sk-tok", "t")

	// Inject 3 chunks into the mock provider.
	app.Provider.StreamChunks = []provider.StreamChunk{
		{ID: "chunk1", Object: "chat.completion.chunk", Model: "m", Choices: []provider.StreamChoice{{Index: 0, Delta: provider.Message{Role: "assistant", Content: "Hello"}}}},
		{ID: "chunk2", Object: "chat.completion.chunk", Model: "m", Choices: []provider.StreamChoice{{Index: 0, Delta: provider.Message{Content: " world"}}}},
		{ID: "chunk3", Object: "chat.completion.chunk", Model: "m", Choices: []provider.StreamChoice{{Index: 0, Delta: provider.Message{}, FinishReason: "stop"}}, Usage: &provider.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}},
	}

	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-tok")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type: %q", got)
	}
	// Each chunk produces "data: {json}\n\n" + a final "data: [DONE]\n\n".
	if !strings.Contains(rec.Body.String(), `"Hello"`) {
		t.Fatalf("missing first chunk: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `" world"`) {
		t.Fatalf("missing second chunk")
	}
	if !strings.Contains(rec.Body.String(), `"stop"`) {
		t.Fatalf("missing finish reason")
	}
	if !strings.HasSuffix(rec.Body.String(), "data: [DONE]\n\n") {
		t.Fatalf("missing [DONE] terminator: %q", rec.Body.String())
	}
}

func TestChat_StreamingUpstreamError(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-aaaa")
	app.AddToken("sk-tok", "t")
	app.Provider.StreamErr = errors.New("upstream died")

	body := `{"model":"m","stream":true,"messages":[]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-tok")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "event: error") {
		t.Fatalf("expected error frame, got: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "upstream died") {
		t.Fatalf("error message not in body")
	}
}
