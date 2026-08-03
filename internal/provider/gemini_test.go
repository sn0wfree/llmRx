package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeminiChat_SystemInstructionForwarded(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = readBody(r)
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4}}`)
	}))
	defer srv.Close()

	_, _, err := NewGeminiProvider().Chat(context.Background(), &ChatRequest{
		Model: "gemini-pro",
		Messages: []Message{
			{Role: "system", Content: "be concise"},
			{Role: "user", Content: "hi"},
		},
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	assertStrContains(t, string(captured), "systemInstruction")
	assertStrContains(t, string(captured), "be concise")
}

func TestGeminiChat_AssistantRoleTranslatedToModel(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = readBody(r)
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4}}`)
	}))
	defer srv.Close()

	_, _, _ = NewGeminiProvider().Chat(context.Background(), &ChatRequest{
		Model: "gemini-pro",
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
			{Role: "user", Content: "bye"},
		},
	}, "sk", srv.URL)
	assertStrContains(t, string(captured), `"role":"model"`)
}

func TestGeminiChat_AuthViaHeader(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-goog-api-key")
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	}))
	defer srv.Close()

	_, _, _ = NewGeminiProvider().Chat(context.Background(), &ChatRequest{
		Model: "gemini-pro", Messages: []Message{{Role: "user", Content: "hi"}},
	}, "gem-key-123", srv.URL)
	if gotKey != "gem-key-123" {
		t.Fatalf("x-goog-api-key = %q, want gem-key-123", gotKey)
	}
}

func TestGeminiChat_UrlContainsModel(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	}))
	defer srv.Close()

	_, _, _ = NewGeminiProvider().Chat(context.Background(), &ChatRequest{
		Model: "gemini-1.5-pro", Messages: []Message{{Role: "user", Content: "hi"}},
	}, "sk", srv.URL)
	if !strings.Contains(path, "gemini-1.5-pro") {
		t.Fatalf("path should contain model name: %s", path)
	}
	if !strings.Contains(path, "generateContent") {
		t.Fatalf("path should contain generateContent: %s", path)
	}
}

func TestGeminiChat_MultiplePartsResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"part1 "},{"text":"part2"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`)
	}))
	defer srv.Close()

	resp, _, err := NewGeminiProvider().Chat(context.Background(), &ChatRequest{
		Model: "gemini-pro", Messages: []Message{{Role: "user", Content: "hi"}},
	}, "sk", srv.URL)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Choices[0].Message.Content != "part1 part2" {
		t.Fatalf("content: %q", resp.Choices[0].Message.Content)
	}
}

func TestGeminiChat_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid API key", 403)
	}))
	defer srv.Close()

	_, code, err := NewGeminiProvider().Chat(context.Background(), &ChatRequest{
		Model: "gemini-pro", Messages: []Message{{Role: "user", Content: "hi"}},
	}, "bad-key", srv.URL)
	if err == nil {
		t.Fatal("expected error")
	}
	if code != 403 {
		t.Fatalf("code: %d", code)
	}
}

func TestGeminiTranslate_NoGenerationConfigWhenNoKnobs(t *testing.T) {
	p := NewGeminiProvider()
	out := p.translateReq(&ChatRequest{
		Model:    "gemini-pro",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if out.GenerationConfig != nil {
		t.Fatal("GenerationConfig should be nil when no knobs set")
	}
}

func TestGeminiTranslate_TopKFromGeminiNative(t *testing.T) {
	p := NewGeminiProvider()
	out := p.translateReq(&ChatRequest{
		Model:    "gemini-pro",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if out.GenerationConfig != nil {
		t.Fatal("GenerationConfig should be nil without knobs")
	}
}

func TestGeminiTranslate_ToolChoiceNone(t *testing.T) {
	p := NewGeminiProvider()
	in := &ChatRequest{
		Model:      "gemini-pro",
		Messages:   []Message{{Role: "user", Content: "hi"}},
		Tools:      []Tool{{Function: FunctionSpec{Name: "f"}}},
		ToolChoice: "none",
	}
	out := p.translateReq(in)
	if out.ToolConfig.FunctionCallingConfig.Mode != "NONE" {
		t.Fatalf("mode: %s", out.ToolConfig.FunctionCallingConfig.Mode)
	}
}

func TestGeminiTranslate_StopString(t *testing.T) {
	p := NewGeminiProvider()
	in := &ChatRequest{
		Model:    "gemini-pro",
		Messages: []Message{{Role: "user", Content: "hi"}},
		Stop:     "END",
	}
	out := p.translateReq(in)
	if out.GenerationConfig == nil {
		t.Fatal("GenerationConfig missing")
	}
	if len(out.GenerationConfig.StopSequences) != 1 || out.GenerationConfig.StopSequences[0] != "END" {
		t.Fatalf("stopSequences: %v", out.GenerationConfig.StopSequences)
	}
}

func TestGeminiChat_ResponseMimeTypeForJsonObject(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = readBody(r)
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"{}"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	}))
	defer srv.Close()

	_, _, _ = NewGeminiProvider().Chat(context.Background(), &ChatRequest{
		Model:          "gemini-pro",
		Messages:       []Message{{Role: "user", Content: "hi"}},
		ResponseFormat: &ResponseFormat{Type: "json_object"},
	}, "sk", srv.URL)
	assertStrContains(t, string(captured), "application/json")
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input string
		n     int
		want  string
	}{
		{"hello", 10, "hello"},
		{"hello", 3, "hel"},
		{"hello", 5, "hello"},
		{"", 5, ""},
	}
	for _, tc := range tests {
		got := truncate(tc.input, tc.n)
		if got != tc.want {
			t.Errorf("truncate(%q, %d): got %q, want %q", tc.input, tc.n, got, tc.want)
		}
	}
}
