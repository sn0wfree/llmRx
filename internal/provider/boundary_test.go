package provider_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/provider"
)

// TestAnthropicChat_MixedContentBlocks tests with mixed text and tool_use blocks.
func TestAnthropicChat_MixedContentBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hello"},{"type":"tool_use","id":"t1","name":"fn","input":{}},{"type":"text","text":" world"}],"model":"claude-3","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer srv.Close()

	p := provider.NewAnthropicProvider()
	resp, _, err := p.Chat(context.Background(), &provider.ChatRequest{
		Model:     "claude-3",
		Messages:  []provider.Message{{Role: "user", Content: "hi"}},
		MaxTokens: 100,
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatalf("no choices")
	}
}

// TestAnthropicChat_ToolUseOnly tests with only tool_use blocks (no text).
func TestAnthropicChat_ToolUseOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"fn","input":{}}],"model":"claude-3","stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer srv.Close()

	p := provider.NewAnthropicProvider()
	resp, _, err := p.Chat(context.Background(), &provider.ChatRequest{
		Model:     "claude-3",
		Messages:  []provider.Message{{Role: "user", Content: "call fn"}},
		MaxTokens: 100,
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatalf("no choices")
	}
}

// TestOpenAIChat_MalformedJSON tests with invalid JSON response.
func TestOpenAIChat_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{not json`))
	}))
	defer srv.Close()

	p := provider.NewOpenAIProvider()
	_, _, err := p.Chat(context.Background(), &provider.ChatRequest{
		Model:    "gpt-4",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	}, "sk", srv.URL)
	if err == nil {
		t.Fatalf("expected error for malformed JSON")
	}
}

// TestGeminiChat_EmptyParts tests with empty parts array.
func TestGeminiChat_EmptyParts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"candidates":[{"content":{"parts":[],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":0,"totalTokenCount":5}}`))
	}))
	defer srv.Close()

	p := provider.NewGeminiProvider()
	resp, _, err := p.Chat(context.Background(), &provider.ChatRequest{
		Model:     "gemini-pro",
		Messages:  []provider.Message{{Role: "user", Content: "hi"}},
		MaxTokens: 10,
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatalf("no choices")
	}
}

// TestOpenAIStream_MalformedChunk tests with malformed JSON in SSE stream.
func TestOpenAIStream_MalformedChunk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		// Send malformed chunk first, then valid chunk, then DONE
		events := []string{
			"data: {not json}\n\n",
			"data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n",
			"data: [DONE]\n\n",
		}
		for _, e := range events {
			fmt.Fprint(w, e)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer srv.Close()

	p := provider.NewOpenAIProvider()
	ch, err := p.StreamChat(context.Background(), &provider.ChatRequest{
		Model:    "gpt-4",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	var chunks []provider.StreamChunk
	for ev := range ch {
		if ev.Err != nil {
			t.Fatalf("stream error: %v", ev.Err)
		}
		chunks = append(chunks, ev.Chunk)
	}
	if len(chunks) < 1 {
		t.Errorf("expected at least 1 chunk after malformed, got %d", len(chunks))
	}
}

// TestGeminiChat_StopAsSlice tests with Stop as []string.
func TestGeminiChat_StopAsSlice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP"}]}`))
	}))
	defer srv.Close()

	p := provider.NewGeminiProvider()
	resp, _, err := p.Chat(context.Background(), &provider.ChatRequest{
		Model:     "gemini-pro",
		Messages:  []provider.Message{{Role: "user", Content: "hi"}},
		MaxTokens: 10,
		Stop:      []string{"END1", "END2"},
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatalf("no choices")
	}
}
