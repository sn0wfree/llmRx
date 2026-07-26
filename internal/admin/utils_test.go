package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
	t1 := newSessionToken()
	t2 := newSessionToken()
	if t1 == "" || t2 == "" {
		t.Fatal("tokens should not be empty")
	}
	if t1 == t2 {
		t.Fatal("tokens should be unique")
	}
	if len(t1) != 48 {
		t.Fatalf("token length: got %d, want 48", len(t1))
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

func TestItoa(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{-1, "-1"},
		{-42, "-42"},
		{9223372036854775807, "9223372036854775807"},
	}
	for _, tc := range tests {
		got := itoa(tc.input)
		if got != tc.want {
			t.Errorf("itoa(%d): got %q, want %q", tc.input, got, tc.want)
		}
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
