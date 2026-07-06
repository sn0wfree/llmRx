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

func TestOpenAIProvider_StreamChat(t *testing.T) {
	// Stand up a fake upstream that returns a valid OpenAI SSE stream.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.Error(w, "bad path", 404)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		chunks := []string{
			`{"id":"1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`,
			`{"id":"2","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"lo"}}]}`,
			`{"id":"3","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			if fl != nil {
				fl.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	defer srv.Close()

	p := NewOpenAIProvider()
	ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "m", Stream: true, Messages: []Message{{Role: "user", Content: "hi"}}}, "sk-test", srv.URL)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var got []string
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				if len(got) != 3 {
					t.Fatalf("expected 3 chunks, got %d: %v", len(got), got)
				}
				return
			}
			if ev.Err != nil {
				t.Fatalf("unexpected err: %v", ev.Err)
			}
			got = append(got, ev.Chunk.Choices[0].Delta.Content)
		case <-deadline:
			t.Fatal("timeout")
		}
	}
}

func TestOpenAIProvider_StreamChat_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer srv.Close()
	p := NewOpenAIProvider()
	_, err := p.StreamChat(context.Background(), &ChatRequest{Model: "m", Stream: true}, "sk", srv.URL)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("err: %v", err)
	}
}

func TestOpenAIProvider_StreamChat_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		fl.Flush()
		// Hold the connection open until client cancels.
		<-r.Context().Done()
	}))
	defer srv.Close()
	p := NewOpenAIProvider()
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.StreamChat(ctx, &ChatRequest{Model: "m", Stream: true}, "sk", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	ev, ok := <-ch
	if !ok {
		return
	}
	if ev.Err == nil {
		t.Fatal("expected error after cancel")
	}
}

func TestAnthropicProvider_Chat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("anthropic-version"), "2023") {
			t.Errorf("missing anthropic-version header")
		}
		if r.Header.Get("x-api-key") == "" {
			t.Errorf("missing x-api-key")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_1","model":"claude-3","content":[{"type":"text","text":"hi from claude"}],"usage":{"input_tokens":5,"output_tokens":3},"stop_reason":"end_turn"}`)
	}))
	defer srv.Close()
	p := NewAnthropicProvider()
	resp, code, err := p.Chat(&ChatRequest{
		Model: "claude-3",
		Messages: []Message{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "hello"},
		},
		MaxTokens: 100,
	}, "sk-test", srv.URL)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if code != 200 {
		t.Fatalf("code: %d", code)
	}
	if resp.Choices[0].Message.Content != "hi from claude" {
		t.Fatalf("content: %q", resp.Choices[0].Message.Content)
	}
	if resp.Usage.PromptTokens != 5 || resp.Usage.CompletionTokens != 3 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

func TestGeminiProvider_Chat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "key=") {
			t.Errorf("missing key= query param: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"hi from gemini"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":4,"totalTokenCount":11}}`)
	}))
	defer srv.Close()
	p := NewGeminiProvider()
	resp, code, err := p.Chat(&ChatRequest{
		Model: "gemini-pro",
		Messages: []Message{
			{Role: "user", Content: "hello"},
		},
	}, "sk-test", srv.URL)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if code != 200 {
		t.Fatalf("code: %d", code)
	}
	if resp.Choices[0].Message.Content != "hi from gemini" {
		t.Fatalf("content: %q", resp.Choices[0].Message.Content)
	}
	if resp.Usage.PromptTokens != 7 || resp.Usage.CompletionTokens != 4 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

func TestProviderFactory(t *testing.T) {
	cases := []struct {
		proto string
		want  string
	}{
		{"openai", "openai-compatible"},
		{"anthropic", "anthropic"},
		{"gemini", "gemini"},
		{"", "openai-compatible"},
		{"unknown", "openai-compatible"},
	}
	for _, tc := range cases {
		got := Factory(tc.proto).Name()
		if got != tc.want {
			t.Errorf("Factory(%q) = %q, want %q", tc.proto, got, tc.want)
		}
	}
}
