package provider

import (
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
