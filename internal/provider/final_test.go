package provider_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/provider"
)

func TestGeminiChat_SystemInstruction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7}}`))
	}))
	defer srv.Close()

	p := provider.NewGeminiProvider()
	resp, status, err := p.Chat(context.Background(), &provider.ChatRequest{
		Model: "gemini-pro",
		Messages: []provider.Message{
			{Role: "system", Content: "You are helpful"},
			{Role: "user", Content: "hi"},
		},
		MaxTokens: 10,
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if status != 200 {
		t.Fatalf("status=%d", status)
	}
	if len(resp.Choices) == 0 {
		t.Fatalf("no choices")
	}
}

func TestGeminiChat_MultimodalContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP"}]}`))
	}))
	defer srv.Close()

	p := provider.NewGeminiProvider()
	resp, _, err := p.Chat(context.Background(), &provider.ChatRequest{
		Model: "gemini-pro",
		Messages: []provider.Message{
			{Role: "user", Content: []provider.ContentPart{{Type: "text", Text: "describe this"}}},
		},
		MaxTokens: 10,
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatalf("no choices")
	}
}

func TestAnthropicChat_Metadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-3","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2}}`))
	}))
	defer srv.Close()

	p := provider.NewAnthropicProvider()
	resp, _, err := p.Chat(context.Background(), &provider.ChatRequest{
		Model:    "claude-3",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		MaxTokens: 10,
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.ID != "msg_1" {
		t.Errorf("id=%q", resp.ID)
	}
}

func TestAnthropicChat_ToolUseResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"Let me call that"},{"type":"tool_use","id":"tool_1","name":"get_weather","input":{"location":"SF"}}],"model":"claude-3","stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer srv.Close()

	p := provider.NewAnthropicProvider()
	resp, _, err := p.Chat(context.Background(), &provider.ChatRequest{
		Model:    "claude-3",
		Messages: []provider.Message{{Role: "user", Content: "what's the weather?"}},
		MaxTokens: 100,
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatalf("no choices")
	}
}

func TestOpenAIChat_DefaultResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`))
	}))
	defer srv.Close()

	p := provider.NewOpenAIProvider()
	resp, status, err := p.Chat(context.Background(), &provider.ChatRequest{
		Model:    "gpt-4",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if status != 200 {
		t.Fatalf("status=%d", status)
	}
	if resp.ID != "c1" {
		t.Errorf("id=%q", resp.ID)
	}
}

func TestOpenAIChat_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":{"message":"overloaded","type":"overloaded_error"}}`))
	}))
	defer srv.Close()

	p := provider.NewOpenAIProvider()
	_, status, err := p.Chat(context.Background(), &provider.ChatRequest{
		Model:    "gpt-4",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	}, "sk", srv.URL)
	if err == nil {
		t.Fatalf("expected error")
	}
	if status != 503 {
		t.Errorf("status=%d want 503", status)
	}
}
