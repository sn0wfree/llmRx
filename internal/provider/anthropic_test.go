package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAnthropicChat_WithTools(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = readBody(r)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_1","model":"claude-3","content":[{"type":"text","text":"result"}],"usage":{"input_tokens":5,"output_tokens":3},"stop_reason":"end_turn"}`)
	}))
	defer srv.Close()

	temp := 0.7
	_, _, err := NewAnthropicProvider().Chat(context.Background(), &ChatRequest{
		Model:       "claude-3",
		Messages:    []Message{{Role: "user", Content: "search for x"}},
		MaxTokens:   256,
		Temperature: &temp,
		Tools: []Tool{{Type: "function", Function: FunctionSpec{
			Name:        "search",
			Description: "web search",
			Parameters:  map[string]any{"type": "object"},
		}}},
		ToolChoice: "auto",
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	assertStrContains(t, string(captured), `"tools"`)
	assertStrContains(t, string(captured), `"search"`)
	assertStrContains(t, string(captured), `"input_schema"`)
	assertStrContains(t, string(captured), `"tool_choice"`)
}

func TestAnthropicChat_AuthHeaders(t *testing.T) {
	var gotKey, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		fmt.Fprint(w, `{"id":"m","model":"c","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1},"stop_reason":"stop"}`)
	}))
	defer srv.Close()

	_, _, _ = NewAnthropicProvider().Chat(context.Background(), &ChatRequest{
		Model: "claude-3", Messages: []Message{{Role: "user", Content: "hi"}}, MaxTokens: 10,
	}, "sk-ant-key", srv.URL)
	if gotKey != "sk-ant-key" {
		t.Fatalf("x-api-key: %q", gotKey)
	}
	if !strings.Contains(gotVersion, "2023") {
		t.Fatalf("anthropic-version: %q", gotVersion)
	}
}

func TestAnthropicChat_Endpoint(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		fmt.Fprint(w, `{"id":"m","model":"c","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1},"stop_reason":"stop"}`)
	}))
	defer srv.Close()

	_, _, _ = NewAnthropicProvider().Chat(context.Background(), &ChatRequest{
		Model: "claude-3", Messages: []Message{{Role: "user", Content: "hi"}}, MaxTokens: 10,
	}, "sk", srv.URL)
	if path != "/v1/messages" {
		t.Fatalf("path: %q, want /v1/messages", path)
	}
}

func TestAnthropicChat_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"overloaded"}`, 529)
	}))
	defer srv.Close()

	_, code, err := NewAnthropicProvider().Chat(context.Background(), &ChatRequest{
		Model: "claude-3", Messages: []Message{{Role: "user", Content: "hi"}}, MaxTokens: 10,
	}, "sk", srv.URL)
	if err == nil {
		t.Fatal("expected error")
	}
	if code != 529 {
		t.Fatalf("code: %d", code)
	}
}

func TestAnthropicChat_DefaultMaxTokens(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = readBody(r)
		fmt.Fprint(w, `{"id":"m","model":"c","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1},"stop_reason":"stop"}`)
	}))
	defer srv.Close()

	_, _, _ = NewAnthropicProvider().Chat(context.Background(), &ChatRequest{
		Model: "claude-3", Messages: []Message{{Role: "user", Content: "hi"}},
	}, "sk", srv.URL)
	assertStrContains(t, string(captured), `"max_tokens":1024`)
}

func TestAnthropicStream_EndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		events := []string{
			"event: message_start\ndata: {\"message\":{\"id\":\"msg_1\",\"model\":\"claude-3\"}}\n",
			"event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n",
			"event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\" world\"}}\n",
			"event: message_delta\ndata: {\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}\n",
			"event: message_stop\ndata: {}\n",
		}
		for _, e := range events {
			fmt.Fprint(w, e+"\n")
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer srv.Close()

	ch, err := NewAnthropicProvider().StreamChat(context.Background(), &ChatRequest{
		Model: "claude-3", Messages: []Message{{Role: "user", Content: "hi"}}, MaxTokens: 10,
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	var contents []string
	var gotUsage *Usage
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				if len(contents) < 2 {
					t.Fatalf("expected at least 2 content chunks, got %d: %v", len(contents), contents)
				}
				if gotUsage == nil {
					t.Fatal("expected usage from message_delta")
				}
				if gotUsage.PromptTokens != 5 || gotUsage.CompletionTokens != 2 {
					t.Fatalf("usage: %+v", gotUsage)
				}
				return
			}
			if ev.Err != nil {
				t.Fatalf("err: %v", ev.Err)
			}
			if len(ev.Chunk.Choices) > 0 {
				c := ev.Chunk.Choices[0].Delta.ContentString()
				if c != "" {
					contents = append(contents, c)
				}
				if ev.Chunk.Usage != nil {
					gotUsage = ev.Chunk.Usage
				}
			}
		case <-deadline:
			t.Fatal("timeout")
		}
	}
}

func TestAnthropicStream_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "overloaded", 529)
	}))
	defer srv.Close()

	_, err := NewAnthropicProvider().StreamChat(context.Background(), &ChatRequest{
		Model: "claude-3", Messages: []Message{{Role: "user", Content: "hi"}}, MaxTokens: 10,
	}, "sk", srv.URL)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "529") {
		t.Fatalf("err: %v", err)
	}
}

func TestAnthropicTranslate_MaxCompletionTokens(t *testing.T) {
	p := NewAnthropicProvider()
	out := p.translateReq(&ChatRequest{
		Model:               "claude-3",
		Messages:            []Message{{Role: "user", Content: "hi"}},
		MaxCompletionTokens: 2048,
	})
	if out.MaxTokens != 2048 {
		t.Fatalf("max_tokens from max_completion_tokens: got %d", out.MaxTokens)
	}
}

func TestAnthropicTranslate_MaxTokensOverCompletion(t *testing.T) {
	p := NewAnthropicProvider()
	out := p.translateReq(&ChatRequest{
		Model:               "claude-3",
		Messages:            []Message{{Role: "user", Content: "hi"}},
		MaxTokens:           512,
		MaxCompletionTokens: 2048,
	})
	if out.MaxTokens != 2048 {
		t.Fatalf("max_completion_tokens should override max_tokens: got %d", out.MaxTokens)
	}
}

func TestAnthropicTranslate_AssistantRoleWithToolCalls(t *testing.T) {
	p := NewAnthropicProvider()
	in := &ChatRequest{
		Model: "claude-3",
		Messages: []Message{
			{Role: "assistant", ToolCalls: []ToolCall{
				{ID: "call_1", Type: "function", Function: FunctionCall{Name: "search", Arguments: `{}`}},
			}},
			{Role: "tool", Content: "result", ToolCallID: "call_1"},
		},
		MaxTokens: 10,
	}
	out := p.translateReq(in)
	if len(out.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out.Messages))
	}
}

func TestAnthropicTranslate_MetadataForwarded(t *testing.T) {
	p := NewAnthropicProvider()
	in := &ChatRequest{
		Model:    "claude-3",
		Messages: []Message{{Role: "user", Content: "hi"}},
		Metadata: map[string]any{"user_id": "u123"},
	}
	out := p.translateReq(in)
	if out.Metadata["user_id"] != "u123" {
		t.Fatalf("metadata: %v", out.Metadata)
	}
}

func TestAnthropicChat_ToolUseResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_1","model":"claude-3","content":[{"type":"tool_use","id":"tool_1","name":"get_weather","input":{"city":"SF"}}],"usage":{"input_tokens":10,"output_tokens":5},"stop_reason":"tool_use"}`)
	}))
	defer srv.Close()

	resp, _, err := NewAnthropicProvider().Chat(context.Background(), &ChatRequest{
		Model: "claude-3", Messages: []Message{{Role: "user", Content: "weather"}}, MaxTokens: 100,
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Choices[0].FinishReason != "tool_use" {
		t.Fatalf("finish_reason: %q", resp.Choices[0].FinishReason)
	}
}

func TestAnthropicTranslate_CacheControlOnBothSystemAndMessage(t *testing.T) {
	p := NewAnthropicProvider()
	in := &ChatRequest{
		Model: "claude-3",
		Messages: []Message{
			{Role: "system", Content: "system prompt", CacheControl: &CacheCtl{Type: "ephemeral"}},
			{Role: "user", Content: "user msg", CacheControl: &CacheCtl{Type: "ephemeral"}},
		},
		MaxTokens: 10,
	}
	out := p.translateReq(in)
	if _, ok := out.System.([]anthropicSystemBlock); !ok {
		t.Fatalf("system should be array form: %T", out.System)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("messages: %d", len(out.Messages))
	}
	parts, ok := out.Messages[0].Content.([]anthropicContentBlock)
	if !ok {
		t.Fatalf("content should be array form: %T", out.Messages[0].Content)
	}
	if len(parts) != 1 || parts[0].CacheControl == nil {
		t.Fatalf("cache_control missing on message: %+v", parts)
	}
}
