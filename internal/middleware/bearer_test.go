package middleware

import (
	"net/http/httptest"
	"testing"
)

func TestExtractBearer_NoHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	if got := extractBearer(req); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractBearer_WithBearer(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer abc123")
	if got := extractBearer(req); got != "abc123" {
		t.Errorf("got %q, want abc123", got)
	}
}

func TestExtractBearer_LowercaseBearer(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "bearer xyz")
	if got := extractBearer(req); got != "" {
		t.Errorf("got %q, want empty (case-sensitive)", got)
	}
}

func TestExtractBearer_BasicAuth(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	if got := extractBearer(req); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractBearer_BearerWithTrailingSpace(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer ")
	if got := extractBearer(req); got != "" {
		t.Errorf("got %q, want empty (no token after space)", got)
	}
}

func TestExtractBearer_JustBearer(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer")
	if got := extractBearer(req); got != "" {
		t.Errorf("got %q, want empty (no space)", got)
	}
}

func TestExtractBearer_LongToken(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	token := "sk-1234567890abcdefghijklmnopqrstuvwxyz"
	req.Header.Set("Authorization", "Bearer "+token)
	if got := extractBearer(req); got != token {
		t.Errorf("got %q, want %q", got, token)
	}
}

func TestExtractBearer_ExtraSpaceAfterPrefix(t *testing.T) {
	// "Bearer  " — extra trailing space after "Bearer " prefix is
	// preserved (whitespace stripped only on the prefix). The
	// implementation trims ONLY the literal "Bearer " prefix, so
	// "Bearer  " yields " " (one space). Document the behavior.
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer  ")
	if got := extractBearer(req); got != " " {
		t.Errorf("got %q, want ' ' (one space)", got)
	}
}
