package auth

import (
	"strings"
	"testing"
)

// TestRandomTokenHex_ZeroAndOneByte pins the boundary sizes: the
// function must handle n=0 (empty string, no error) and n=1 (2 chars)
// — the 25% uncovered branch is the rand.Read error path, which
// cannot be triggered without replacing crypto/rand; these at least
// cover the size handling around it.
func TestRandomTokenHex_ZeroAndOneByte(t *testing.T) {
	empty, err := RandomTokenHex(0)
	if err != nil {
		t.Fatalf("n=0: %v", err)
	}
	if empty != "" {
		t.Fatalf("n=0: got %q, want empty", empty)
	}

	one, err := RandomTokenHex(1)
	if err != nil {
		t.Fatalf("n=1: %v", err)
	}
	if len(one) != 2 {
		t.Fatalf("n=1: len = %d, want 2", len(one))
	}
}

// TestNewAPIKey_EntropyDiffers: two keys never share the payload.
func TestNewAPIKey_EntropyDiffers(t *testing.T) {
	a, err := NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	b, err := NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if strings.TrimPrefix(a, "sk-") == strings.TrimPrefix(b, "sk-") {
		t.Fatal("two API keys share the same entropy payload")
	}
}
