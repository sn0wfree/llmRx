package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAll_ReturnsAllProtocols(t *testing.T) {
	m := All()
	expected := []string{
		"openai", "openai-compatible",
		"anthropic", "anthropic-messages",
		"gemini", "google-gemini",
	}
	for _, proto := range expected {
		if _, ok := m[proto]; !ok {
			t.Errorf("All() missing protocol %q", proto)
		}
	}
}

func TestAll_ReturnsNonNil(t *testing.T) {
	m := All()
	if m == nil {
		t.Fatal("All() should not return nil")
	}
	if len(m) < 6 {
		t.Fatalf("expected at least 6 providers, got %d", len(m))
	}
}

func TestAll_EachProviderHasName(t *testing.T) {
	m := All()
	for proto, p := range m {
		if p == nil {
			t.Errorf("provider for %q is nil", proto)
			continue
		}
		if p.Name() == "" {
			t.Errorf("provider for %q has empty Name()", proto)
		}
	}
}

func TestLowerFirst(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"Hello", "hello"},
		{"World", "world"},
		{"a", "a"},
		{"ABC", "aBC"},
	}
	for _, tc := range tests {
		got := lowerFirst(tc.input)
		if got != tc.want {
			t.Errorf("lowerFirst(%q): got %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ---------- Registry tests ----------

func TestFactory_ResolvesProviderName(t *testing.T) {
	// "minimax" is a built-in provider that maps to the "openai" protocol
	p := Factory("minimax")
	if p == nil {
		t.Fatal("Factory(minimax) returned nil")
	}
	if p.Name() != "openai-compatible" {
		t.Errorf("Factory(minimax).Name() = %q, want %q", p.Name(), "openai-compatible")
	}
}

func TestFactory_ResolvesProtocolName(t *testing.T) {
	// "anthropic" is both a provider name and a protocol name
	p := Factory("anthropic")
	if p == nil {
		t.Fatal("Factory(anthropic) returned nil")
	}
	if p.Name() != "anthropic" {
		t.Errorf("Factory(anthropic).Name() = %q, want %q", p.Name(), "anthropic")
	}
}

func TestFactory_UnknownFallsBackToOpenAI(t *testing.T) {
	p := Factory("nonexistent-provider")
	if p == nil {
		t.Fatal("Factory(unknown) returned nil")
	}
	if p.Name() != "openai-compatible" {
		t.Errorf("Factory(unknown).Name() = %q, want %q", p.Name(), "openai-compatible")
	}
}

func TestAllProviders_IncludesBuiltins(t *testing.T) {
	descs := AllProviders()
	names := map[string]bool{}
	for _, d := range descs {
		names[d.Name] = true
	}
	for _, expected := range []string{"openai", "anthropic", "gemini", "minimax", "deepseek", "moonshot", "qwen", "zhipu", "volcengine"} {
		if !names[expected] {
			t.Errorf("AllProviders() missing %q", expected)
		}
	}
}

func TestLookupProvider(t *testing.T) {
	d, ok := LookupProvider("minimax")
	if !ok {
		t.Fatal("LookupProvider(minimax) not found")
	}
	if d.Protocol != "openai" {
		t.Errorf("minimax protocol: got %q, want %q", d.Protocol, "openai")
	}
	if d.DefaultBaseURL != "https://api.minimax.io/v1" {
		t.Errorf("minimax base_url: got %q", d.DefaultBaseURL)
	}
}

func TestRegisterProvider_OverwritesExisting(t *testing.T) {
	original, _ := LookupProvider("minimax")
	defer RegisterProvider(original)

	RegisterProvider(ProviderDesc{
		Name:           "minimax",
		DisplayName:    "MiniMax Override",
		Protocol:       "openai",
		DefaultBaseURL: "https://override.example.com/v1",
	})
	d, ok := LookupProvider("minimax")
	if !ok {
		t.Fatal("LookupProvider(minimax) not found after override")
	}
	if d.DefaultBaseURL != "https://override.example.com/v1" {
		t.Errorf("overridden base_url: got %q", d.DefaultBaseURL)
	}
}

func TestLoadProvidersFromYAML(t *testing.T) {
	yaml := []byte(`
providers:
  - name: test-provider-yaml
    display_name: Test Provider
    protocol: openai
    base_url: https://test.example.com/v1
`)
	if err := LoadProvidersFromYAML(yaml, "config"); err != nil {
		t.Fatalf("LoadProvidersFromYAML: %v", err)
	}
	d, ok := LookupProvider("test-provider-yaml")
	if !ok {
		t.Fatal("test-provider-yaml not found after loading")
	}
	if d.DisplayName != "Test Provider" {
		t.Errorf("display_name: got %q", d.DisplayName)
	}
	if d.Source != "config" {
		t.Errorf("source: got %q, want %q", d.Source, "config")
	}
}

// ---------- ListModels tests ----------

func TestListModels_OpenAICompatible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path: got %q, want /models", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth header: got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"id": "MiniMax-M3", "object": "model"},
				{"id": "MiniMax-M2.7", "object": "model"},
				{"id": "deepseek-chat", "object": "model"},
			},
		})
	}))
	defer srv.Close()

	models, err := ListModels(context.Background(), "minimax", "test-key", srv.URL)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d: %v", len(models), models)
	}
	expected := []string{"MiniMax-M3", "MiniMax-M2.7", "deepseek-chat"}
	for i, m := range models {
		if m != expected[i] {
			t.Errorf("model[%d]: got %q, want %q", i, m, expected[i])
		}
	}
}

func TestListModels_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "invalid key"}`))
	}))
	defer srv.Close()

	_, err := ListModels(context.Background(), "openai", "bad-key", srv.URL)
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}
