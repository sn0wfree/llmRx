package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIChat_ToolCallResponsePassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"c1","object":"chat.completion","model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	}))
	defer srv.Close()

	resp, code, err := NewOpenAIProvider().Chat(context.Background(), &ChatRequest{
		Model:    "gpt-4",
		Messages: []Message{{Role: "user", Content: "weather"}},
		Tools:    []Tool{{Type: "function", Function: FunctionSpec{Name: "get_weather"}}},
	}, "sk", srv.URL)
	if err != nil || code != 200 {
		t.Fatalf("chat: err=%v code=%d", err, code)
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish_reason: %q", resp.Choices[0].FinishReason)
	}
	tc := resp.Choices[0].Message.ToolCalls
	if len(tc) != 1 || tc[0].ID != "call_abc" || tc[0].Function.Name != "get_weather" {
		t.Fatalf("tool_calls: %+v", tc)
	}
	if tc[0].Function.Arguments == "" {
		t.Fatal("arguments should not be empty")
	}
}

func TestOpenAIChat_ResponseFormatJsonSchema(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = readBody(r)
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","model":"m","choices":[],"usage":{}}`)
	}))
	defer srv.Close()

	schema := map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}}
	_, _, _ = NewOpenAIProvider().Chat(context.Background(), &ChatRequest{
		Model:          "gpt-4",
		Messages:       []Message{{Role: "user", Content: "hi"}},
		ResponseFormat: &ResponseFormat{Type: "json_schema", JSONSchema: &JSONSchemaCfg{Name: "out", Schema: schema, Strict: BoolPtr(true)}},
	}, "sk", srv.URL)

	assertStrContains(t, string(captured), "json_schema")
	assertStrContains(t, string(captured), "strict")
}

func TestOpenAIChat_ParallelToolCalls(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = readBody(r)
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","model":"m","choices":[],"usage":{}}`)
	}))
	defer srv.Close()

	_, _, _ = NewOpenAIProvider().Chat(context.Background(), &ChatRequest{
		Model:             "gpt-4",
		Messages:          []Message{{Role: "user", Content: "hi"}},
		ParallelToolCalls: BoolPtr(true),
	}, "sk", srv.URL)
	assertJSONContains(t, captured, "parallel_tool_calls")
}

func TestOpenAIChat_FrequencyPresencePenalty(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = readBody(r)
		fmt.Fprint(w, `{"id":"x","choices":[],"usage":{}}`)
	}))
	defer srv.Close()

	fp := 0.5
	pp := 0.3
	_, _, _ = NewOpenAIProvider().Chat(context.Background(), &ChatRequest{
		Model:            "gpt-4",
		Messages:         []Message{{Role: "user", Content: "hi"}},
		FrequencyPenalty: &fp,
		PresencePenalty:  &pp,
	}, "sk", srv.URL)
	assertJSONContains(t, captured, "frequency_penalty")
	assertJSONContains(t, captured, "presence_penalty")
}

func TestOpenAIChat_LogitBiasAndLogprobs(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = readBody(r)
		fmt.Fprint(w, `{"id":"x","choices":[],"usage":{}}`)
	}))
	defer srv.Close()

	_, _, _ = NewOpenAIProvider().Chat(context.Background(), &ChatRequest{
		Model:       "gpt-4",
		Messages:    []Message{{Role: "user", Content: "hi"}},
		LogitBias:   map[string]int{"123": -100},
		Logprobs:    BoolPtr(true),
		TopLogprobs: IntPtr(5),
	}, "sk", srv.URL)
	assertJSONContains(t, captured, "logit_bias")
	assertJSONContains(t, captured, "logprobs")
	assertJSONContains(t, captured, "top_logprobs")
}

func TestOpenAIChat_StoreAndReasoningEffort(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = readBody(r)
		fmt.Fprint(w, `{"id":"x","choices":[],"usage":{}}`)
	}))
	defer srv.Close()

	_, _, _ = NewOpenAIProvider().Chat(context.Background(), &ChatRequest{
		Model:           "o1",
		Messages:        []Message{{Role: "user", Content: "hi"}},
		Store:           BoolPtr(true),
		ReasoningEffort: "high",
	}, "sk", srv.URL)
	assertJSONContains(t, captured, "store")
	assertJSONContains(t, captured, "reasoning_effort")
}

func TestOpenAIChat_PromptCacheKey(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = readBody(r)
		fmt.Fprint(w, `{"id":"x","choices":[],"usage":{}}`)
	}))
	defer srv.Close()

	_, _, _ = NewOpenAIProvider().Chat(context.Background(), &ChatRequest{
		Model:          "claude-3",
		Messages:       []Message{{Role: "user", Content: "hi"}},
		PromptCacheKey: "cache-123",
	}, "sk", srv.URL)
	assertJSONContains(t, captured, "prompt_cache_key")
}

func TestOpenAIChat_CachedTokensInResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"prompt_tokens_details":{"cached_tokens":50,"audio_tokens":0}}}`)
	}))
	defer srv.Close()

	resp, _, err := NewOpenAIProvider().Chat(context.Background(), &ChatRequest{
		Model:    "gpt-4",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Usage.PromptTokensDetails == nil {
		t.Fatal("PromptTokensDetails should not be nil")
	}
	if resp.Usage.PromptTokensDetails.CachedTokens != 50 {
		t.Fatalf("cached_tokens: got %d, want 50", resp.Usage.PromptTokensDetails.CachedTokens)
	}
}

func TestOpenAIChat_RefusalPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"","refusal":"I cannot help"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
	}))
	defer srv.Close()

	resp, _, err := NewOpenAIProvider().Chat(context.Background(), &ChatRequest{
		Model:    "gpt-4",
		Messages: []Message{{Role: "user", Content: "bad request"}},
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Choices[0].Message.Refusal != "I cannot help" {
		t.Fatalf("refusal: %q", resp.Choices[0].Message.Refusal)
	}
}

func TestOpenAIChat_ToolCallIDForwarded(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = readBody(r)
		fmt.Fprint(w, `{"id":"x","choices":[],"usage":{}}`)
	}))
	defer srv.Close()

	_, _, _ = NewOpenAIProvider().Chat(context.Background(), &ChatRequest{
		Model: "gpt-4",
		Messages: []Message{
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Function: FunctionCall{Name: "get_weather", Arguments: `{}`}}}},
			{Role: "tool", Content: "sunny", ToolCallID: "call_1"},
		},
	}, "sk", srv.URL)
	assertStrContains(t, string(captured), "tool_call_id")
	assertStrContains(t, string(captured), "call_1")
}
