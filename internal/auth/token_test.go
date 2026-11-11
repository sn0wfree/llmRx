package auth

import (
	"encoding/hex"
	"strings"
	"testing"
)

// TestRandomTokenHex_LengthAndUniqueness: 32 bytes → 64 hex chars,
// and successive calls never repeat.
func TestRandomTokenHex_LengthAndUniqueness(t *testing.T) {
	seen := make(map[string]bool, 500)
	for i := 0; i < 500; i++ {
		tok, err := RandomTokenHex(32)
		if err != nil {
			t.Fatalf("RandomTokenHex: %v", err)
		}
		if len(tok) != 64 {
			t.Fatalf("len = %d, want 64", len(tok))
		}
		if _, err := hex.DecodeString(tok); err != nil {
			t.Fatalf("not valid hex: %v", err)
		}
		if seen[tok] {
			t.Fatal("duplicate token generated")
		}
		seen[tok] = true
	}
}

// TestNewSessionToken: the standard session token shape.
func TestNewSessionToken(t *testing.T) {
	tok, err := NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	if len(tok) != 64 {
		t.Fatalf("len = %d, want 64", len(tok))
	}
}

// TestNewAPIKey: sk- prefix + full entropy.
func TestNewAPIKey(t *testing.T) {
	key, err := NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if !strings.HasPrefix(key, "sk-") {
		t.Fatalf("missing sk- prefix: %q", key)
	}
	if len(key) != 67 {
		t.Fatalf("len = %d, want 67", len(key))
	}
}
