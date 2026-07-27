package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOpenAIProvider_Chat_Non200 covers the upstream error path
// (StatusCode != 200 → returns wrapped error with status code + body).
func TestOpenAIProvider_Chat_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": "bad request"}`))
	}))
	defer srv.Close()
	p := NewOpenAIProvider()
	_, code, err := p.Chat(context.Background(), &ChatRequest{Model: "m"}, "sk-test", srv.URL)
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", code)
	}
	if !strings.Contains(err.Error(), "upstream 400") {
		t.Errorf("err = %v, want upstream 400 prefix", err)
	}
}

func TestOpenAIProvider_Chat_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	p := NewOpenAIProvider()
	_, code, err := p.Chat(context.Background(), &ChatRequest{Model: "m"}, "sk-test", srv.URL)
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	if code != http.StatusOK {
		t.Errorf("code = %d, want 200", code)
	}
	if !strings.Contains(err.Error(), "unmarshal") {
		t.Errorf("err = %v, want unmarshal prefix", err)
	}
}

func TestOpenAIProvider_Chat_BearerAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"id":"x","choices":[]}`))
	}))
	defer srv.Close()
	p := NewOpenAIProvider()
	_, _, _ = p.Chat(context.Background(), &ChatRequest{Model: "m"}, "sk-1234", srv.URL)
	if gotAuth != "Bearer sk-1234" {
		t.Errorf("Authorization = %q, want Bearer sk-1234", gotAuth)
	}
}

func TestOpenAIProvider_Chat_ContentType(t *testing.T) {
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`{"id":"x","choices":[]}`))
	}))
	defer srv.Close()
	p := NewOpenAIProvider()
	_, _, _ = p.Chat(context.Background(), &ChatRequest{Model: "m"}, "sk-x", srv.URL)
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
}

// --- Anthropic Chat ---

func TestAnthropicProvider_Chat_AuthHeader(t *testing.T) {
	var gotKey, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		_, _ = w.Write([]byte(`{"id":"x","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	p := NewAnthropicProvider()
	_, _, _ = p.Chat(context.Background(), &ChatRequest{Model: "claude-3"}, "sk-ant-test", srv.URL)
	if gotKey != "sk-ant-test" {
		t.Errorf("x-api-key = %q, want sk-ant-test", gotKey)
	}
	if gotVersion != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", gotVersion)
	}
}

func TestAnthropicProvider_Chat_EndpointPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"x","content":[],"stop_reason":"end_turn","usage":{}}`))
	}))
	defer srv.Close()
	p := NewAnthropicProvider()
	_, _, _ = p.Chat(context.Background(), &ChatRequest{Model: "claude-3"}, "sk-x", srv.URL)
	if gotPath != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", gotPath)
	}
}

func TestAnthropicProvider_Chat_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad api key"}`))
	}))
	defer srv.Close()
	p := NewAnthropicProvider()
	_, code, err := p.Chat(context.Background(), &ChatRequest{Model: "claude-3"}, "sk-bad", srv.URL)
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", code)
	}
}

// --- Gemini Chat ---

func TestGeminiProvider_Chat_QueryKey(t *testing.T) {
	var gotKey, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("key")
		gotModel = extractGeminiModel(r.URL.Path)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"hi"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
	}))
	defer srv.Close()
	p := NewGeminiProvider()
	_, _, _ = p.Chat(context.Background(), &ChatRequest{Model: "gemini-1.5-pro"}, "sk-gemini-key", srv.URL)
	if gotKey != "sk-gemini-key" {
		t.Errorf("?key= = %q, want sk-gemini-key", gotKey)
	}
	if gotModel != "gemini-1.5-pro" {
		t.Errorf("model in path = %q, want gemini-1.5-pro", gotModel)
	}
}

func TestGeminiProvider_Chat_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"forbidden"}}`))
	}))
	defer srv.Close()
	p := NewGeminiProvider()
	_, code, err := p.Chat(context.Background(), &ChatRequest{Model: "gemini-1.5-pro"}, "sk-bad", srv.URL)
	if err == nil {
		t.Fatal("expected error on 403")
	}
	if code != http.StatusForbidden {
		t.Errorf("code = %d, want 403", code)
	}
}

// extractGeminiModel pulls "models/<name>:..." from a URL path.
func extractGeminiModel(path string) string {
	const prefix = "/v1beta/models/"
	i := strings.Index(path, prefix)
	if i < 0 {
		return ""
	}
	rest := path[i+len(prefix):]
	if j := strings.Index(rest, ":"); j >= 0 {
		return rest[:j]
	}
	return rest
}
