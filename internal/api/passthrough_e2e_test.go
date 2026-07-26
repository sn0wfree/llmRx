package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

type capturingProvider struct {
	lastReq   *provider.ChatRequest
	resp      *provider.ChatResponse
	status    int
	err       error
	streamChk []provider.StreamChunk
	streamErr error
}

func (c *capturingProvider) Name() string { return "capturing" }
func (c *capturingProvider) Chat(_ context.Context, req *provider.ChatRequest, _, _ string) (*provider.ChatResponse, int, error) {
	c.lastReq = req
	if c.err != nil {
		return nil, c.status, c.err
	}
	if c.resp != nil {
		return c.resp, c.status, nil
	}
	return &provider.ChatResponse{ID: "x", Model: req.Model}, 200, nil
}
func (c *capturingProvider) StreamChat(_ context.Context, req *provider.ChatRequest, _, _ string) (<-chan provider.StreamEvent, error) {
	c.lastReq = req
	if c.streamErr != nil {
		return nil, c.streamErr
	}
	out := make(chan provider.StreamEvent, len(c.streamChk)+1)
	go func() {
		defer close(out)
		for _, chk := range c.streamChk {
			out <- provider.StreamEvent{Chunk: chk}
		}
	}()
	return out, nil
}

func TestGateway_FullRequestPassthrough(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannelWithPrice("c", "openai", "https://x", []string{"gpt-4"}, 1.0, 2.0, "sk-key")
	app.AddToken("sk-t", "t")
	cp := &capturingProvider{}
	app.Chat.SetProviders(map[string]provider.Provider{"": cp, "openai": cp})

	body := `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"temperature":0.5,"top_p":0.9,"max_tokens":100,"seed":42,"tools":[{"type":"function","function":{"name":"f","parameters":{}}}],"tool_choice":"auto","response_format":{"type":"json_object"},"stream_options":{"include_usage":true},"parallel_tool_calls":true}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	if cp.lastReq == nil {
		t.Fatal("provider was not called")
	}
	if cp.lastReq.Model != "gpt-4" {
		t.Errorf("model: %q", cp.lastReq.Model)
	}
	if cp.lastReq.Temperature == nil || *cp.lastReq.Temperature != 0.5 {
		t.Errorf("temperature: %v", cp.lastReq.Temperature)
	}
	if cp.lastReq.TopP == nil || *cp.lastReq.TopP != 0.9 {
		t.Errorf("top_p: %v", cp.lastReq.TopP)
	}
	if cp.lastReq.MaxTokens != 100 {
		t.Errorf("max_tokens: %d", cp.lastReq.MaxTokens)
	}
	if cp.lastReq.Seed == nil || *cp.lastReq.Seed != 42 {
		t.Errorf("seed: %v", cp.lastReq.Seed)
	}
	if len(cp.lastReq.Tools) != 1 || cp.lastReq.Tools[0].Function.Name != "f" {
		t.Errorf("tools: %+v", cp.lastReq.Tools)
	}
	if cp.lastReq.ToolChoice != "auto" {
		t.Errorf("tool_choice: %v", cp.lastReq.ToolChoice)
	}
	if cp.lastReq.ResponseFormat == nil || cp.lastReq.ResponseFormat.Type != "json_object" {
		t.Errorf("response_format: %v", cp.lastReq.ResponseFormat)
	}
	if cp.lastReq.StreamOptions == nil || !cp.lastReq.StreamOptions.IncludeUsage {
		t.Errorf("stream_options: %v", cp.lastReq.StreamOptions)
	}
	if cp.lastReq.ParallelToolCalls == nil || !*cp.lastReq.ParallelToolCalls {
		t.Errorf("parallel_tool_calls: %v", cp.lastReq.ParallelToolCalls)
	}
}

func TestGateway_ToolCallResponsePassthrough(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannelWithPrice("c", "openai", "https://x", []string{"gpt-4"}, 1, 2, "sk-key")
	app.AddToken("sk-t", "t")
	cp := &capturingProvider{
		resp: &provider.ChatResponse{
			ID: "c1", Object: "chat.completion", Model: "gpt-4",
			Choices: []provider.Choice{{
				Index: 0,
				Message: provider.Message{
					Role: "assistant",
					ToolCalls: []provider.ToolCall{
						{ID: "call_1", Type: "function", Function: provider.FunctionCall{Name: "get_weather", Arguments: `{"city":"SF"}`}},
					},
				},
				FinishReason: "tool_calls",
			}},
			Usage: provider.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		},
		status: 200,
	}
	app.Chat.SetProviders(map[string]provider.Provider{"": cp, "openai": cp})

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4","messages":[{"role":"user","content":"weather"}]}`))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: %d", rec.Code)
	}
	var resp provider.ChatResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("response: %+v", resp)
	}
	tc := resp.Choices[0].Message.ToolCalls
	if len(tc) != 1 || tc[0].ID != "call_1" || tc[0].Function.Name != "get_weather" {
		t.Fatalf("tool_calls: %+v", tc)
	}
}

func TestGateway_MultimodalRequestForwarded(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannelWithPrice("c", "openai", "https://x", []string{"gpt-4-vision"}, 1, 1, "sk-key")
	app.AddToken("sk-t", "t")
	cp := &capturingProvider{}
	app.Chat.SetProviders(map[string]provider.Provider{"": cp, "openai": cp})

	body := `{"model":"gpt-4-vision","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"https://x.com/img.png","detail":"high"}}]}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	if cp.lastReq == nil {
		t.Fatal("provider not called")
	}
	if len(cp.lastReq.Messages) != 1 {
		t.Fatalf("messages: %d", len(cp.lastReq.Messages))
	}
	text := cp.lastReq.Messages[0].ContentString()
	if text != "describe" {
		t.Errorf("text content: %q", text)
	}
}

func TestGateway_AnthropicProtocolRouting(t *testing.T) {
	app := testhelper.New(t)
	ch := app.AddChannelWithPrice("c", "anthropic", "https://x", []string{"claude-3"}, 1, 1, "sk-key")
	ch.Protocol = "anthropic"
	app.Store.UpdateChannel(ch)
	app.Pool.LoadFromStore(app.Store)
	app.AddToken("sk-t", "t")
	cp := &capturingProvider{}
	app.Chat.SetProviders(map[string]provider.Provider{"": cp, "anthropic": cp, "openai": cp})

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"claude-3","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	if cp.lastReq == nil {
		t.Fatal("anthropic provider not called")
	}
}

func TestGateway_GeminiProtocolRouting(t *testing.T) {
	app := testhelper.New(t)
	ch := app.AddChannelWithPrice("c", "gemini", "https://x", []string{"gemini-pro"}, 1, 1, "sk-key")
	ch.Protocol = "gemini"
	app.Store.UpdateChannel(ch)
	app.Pool.LoadFromStore(app.Store)
	app.AddToken("sk-t", "t")
	cp := &capturingProvider{}
	app.Chat.SetProviders(map[string]provider.Provider{"": cp, "gemini": cp, "openai": cp})

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"gemini-pro","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	if cp.lastReq == nil {
		t.Fatal("gemini provider not called")
	}
}

func TestGateway_ModelWhitelistEnforced(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannelWithPrice("c", "openai", "https://x", []string{"allowed-model"}, 1, 1, "sk-key")
	tok := app.AddToken("sk-t", "t")
	tok.ModelsWhitelist = []string{"allowed-model"}
	app.Store.UpdateToken(tok)
	app.Cache.Reload()

	cp := &capturingProvider{}
	app.Chat.SetProviders(map[string]provider.Provider{"": cp, "openai": cp})

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"forbidden-model","messages":[]}`))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "model_not_allowed") {
		t.Fatalf("body: %s", rec.Body.String())
	}
}

func TestGateway_StreamingToolCallDelta(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannelWithPrice("c", "openai", "https://x", []string{"m"}, 1, 1, "sk-key")
	app.AddToken("sk-t", "t")

	idx := 0
	cp := &capturingProvider{
		streamChk: []provider.StreamChunk{
			{ID: "1", Object: "chat.completion.chunk", Model: "m", Choices: []provider.StreamChoice{{
				Index: 0, Delta: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{
					{Index: &idx, ID: "call_1", Type: "function", Function: provider.FunctionCall{Name: "f", Arguments: ""}},
				}},
			}}},
			{ID: "2", Object: "chat.completion.chunk", Model: "m", Choices: []provider.StreamChoice{{
				Index: 0, Delta: provider.Message{}, FinishReason: "tool_calls",
			}}},
		},
	}
	app.Chat.SetProviders(map[string]provider.Provider{"": cp, "openai": cp})

	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"call f"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "tool_calls") {
		t.Fatalf("tool_calls not in stream: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "call_1") {
		t.Fatalf("call_1 not in stream: %s", rec.Body.String())
	}
}
