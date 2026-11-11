package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/auth"
)

func TestMaskKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"sk-abcdefghij", "sk-a***ghij"},
		{"short", "short"},
		{"12345678", "12345678"},
		{"123456789", "1234***6789"},
		{"", ""},
	}
	for _, tc := range tests {
		got := maskKey(tc.input)
		if got != tc.want {
			t.Errorf("maskKey(%q): got %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestNewSessionToken(t *testing.T) {
	t1, err := auth.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	t2, err := auth.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	if t1 == "" || t2 == "" {
		t.Fatal("tokens should not be empty")
	}
	if t1 == t2 {
		t.Fatal("tokens should be unique")
	}
	// 32 bytes of entropy hex-encoded = 64 characters.
	if len(t1) != 64 {
		t.Fatalf("token length: got %d, want 64", len(t1))
	}
}

func TestNewAPIToken(t *testing.T) {
	t1, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	t2, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if t1 == "" || t2 == "" {
		t.Fatal("tokens should not be empty")
	}
	if t1 == t2 {
		t.Fatal("tokens should be unique")
	}
	if !strings.HasPrefix(t1, "sk-") {
		t.Fatalf("api token missing sk- prefix: %q", t1)
	}
	if !strings.HasPrefix(t2, "sk-") {
		t.Fatalf("api token missing sk- prefix: %q", t2)
	}
	// "sk-" (3) + 64 hex chars = 67
	if len(t1) != 67 {
		t.Fatalf("api token length: got %d, want 67", len(t1))
	}
}

func TestReadCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "test_cookie", Value: "abc123"})
	if got := readCookie(req, "test_cookie"); got != "abc123" {
		t.Fatalf("readCookie: got %q", got)
	}
	if got := readCookie(req, "nonexistent"); got != "" {
		t.Fatalf("readCookie missing: got %q", got)
	}
}

func TestReadCookie_NoCookies(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := readCookie(req, "anything"); got != "" {
		t.Fatalf("readCookie with no cookies: got %q", got)
	}
}

func TestSetAlertManager(t *testing.T) {
	h := &Handler{}
	h.SetAlertManager(nil)
}

func TestSetSessionTTL(t *testing.T) {
	h := &Handler{}
	h.SetSessionTTL(3600)
}
