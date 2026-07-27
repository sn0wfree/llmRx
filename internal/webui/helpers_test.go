package webui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

func newTestApp(t *testing.T) *store.SQLite {
	t.Helper()
	dir := t.TempDir()
	s, err := store.OpenSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	logDir := filepath.Join(dir, "logs")
	if err := logstore.EnsureDir(logDir); err != nil {
		t.Fatalf("logstore.EnsureDir: %v", err)
	}
	ls, err := logstore.New(logDir, nil)
	if err != nil {
		t.Fatalf("logstore.New: %v", err)
	}
	s.SetLogStore(ls)
	t.Cleanup(func() { _ = ls.Close() })
	s.CreateUser(&model.User{Username: "admin", PasswordHash: "x", Role: model.RoleRoot, Status: 1})
	return s
}

func TestSplitLines(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 1},
		{"a\nb\nc", 3},
		{"a\n\nb", 2},
		{"  a  \n  b  ", 2},
		{"\n\n\n", 0},
	}
	for _, tc := range tests {
		got := splitLines(tc.input)
		if len(got) != tc.want {
			t.Errorf("splitLines(%q): got %d, want %d (%v)", tc.input, len(got), tc.want, got)
		}
	}
}

func TestSplitLines_TrimsWhitespace(t *testing.T) {
	got := splitLines("  hello  \n  world  ")
	if got[0] != "hello" || got[1] != "world" {
		t.Fatalf("expected trimmed lines, got %v", got)
	}
}

func TestParseIntDefault(t *testing.T) {
	if got := parseIntDefault("", 42); got != 42 {
		t.Fatalf("empty: got %d", got)
	}
	if got := parseIntDefault("123", 42); got != 123 {
		t.Fatalf("valid: got %d", got)
	}
	if got := parseIntDefault("abc", 42); got != 42 {
		t.Fatalf("invalid: got %d", got)
	}
	if got := parseIntDefault("-5", 42); got != -5 {
		t.Fatalf("negative: got %d", got)
	}
}

func TestParseFloatDefault(t *testing.T) {
	if got := parseFloatDefault("", 1.5); got != 1.5 {
		t.Fatalf("empty: got %f", got)
	}
	if got := parseFloatDefault("3.14", 1.5); got != 3.14 {
		t.Fatalf("valid: got %f", got)
	}
	if got := parseFloatDefault("abc", 1.5); got != 1.5 {
		t.Fatalf("invalid: got %f", got)
	}
	if got := parseFloatDefault("-0.5", 1.5); got != -0.5 {
		t.Fatalf("negative: got %f", got)
	}
}

func TestFirstOrEmpty(t *testing.T) {
	if got := firstOrEmpty(nil); got != "" {
		t.Fatalf("nil: got %q", got)
	}
	if got := firstOrEmpty([]string{}); got != "" {
		t.Fatalf("empty: got %q", got)
	}
	if got := firstOrEmpty([]string{"a", "b"}); got != "a" {
		t.Fatalf("two: got %q", got)
	}
}

func TestValidateChannel(t *testing.T) {
	tests := []struct {
		name    string
		ch      *model.Channel
		wantErr bool
	}{
		{"valid", &model.Channel{Name: "c", Provider: "p", BaseURL: "u", Models: []string{"m"}}, false},
		{"empty name", &model.Channel{Name: "  ", Provider: "p", BaseURL: "u", Models: []string{"m"}}, true},
		{"empty provider", &model.Channel{Name: "c", Provider: "", BaseURL: "u", Models: []string{"m"}}, true},
		{"empty baseurl", &model.Channel{Name: "c", Provider: "p", BaseURL: "", Models: []string{"m"}}, true},
		{"no models", &model.Channel{Name: "c", Provider: "p", BaseURL: "u", Models: nil}, true},
		{"negative price", &model.Channel{Name: "c", Provider: "p", BaseURL: "u", Models: []string{"m"}, InputPrice: -1}, true},
		{"negative output", &model.Channel{Name: "c", Provider: "p", BaseURL: "u", Models: []string{"m"}, OutputPrice: -0.1}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateChannel(tc.ch)
			if tc.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidationError_Error(t *testing.T) {
	e := errBad("test message")
	if e.Error() != "test message" {
		t.Fatalf("Error(): got %q", e.Error())
	}
}

func TestParseInt64Default(t *testing.T) {
	if got := parseInt64Default("", 99); got != 99 {
		t.Fatalf("empty: got %d", got)
	}
	if got := parseInt64Default("42", 99); got != 42 {
		t.Fatalf("valid: got %d", got)
	}
	if got := parseInt64Default("abc", 99); got != 99 {
		t.Fatalf("invalid: got %d", got)
	}
}

func TestContainsArr(t *testing.T) {
	if !containsArr([]string{"a", "b", "c"}, "b") {
		t.Fatal("expected true")
	}
	if containsArr([]string{"a", "b", "c"}, "d") {
		t.Fatal("expected false")
	}
	if containsArr(nil, "a") {
		t.Fatal("nil: expected false")
	}
}

func TestEscapeHTML(t *testing.T) {
	if got := escapeHTML("hello"); got != "hello" {
		t.Errorf("plain text: got %q", got)
	}
	if got := escapeHTML(""); got != "" {
		t.Errorf("empty: got %q", got)
	}
}

func TestNewSessionToken(t *testing.T) {
	tok1 := newSessionToken()
	tok2 := newSessionToken()
	if tok1 == "" || tok2 == "" {
		t.Fatal("tokens should not be empty")
	}
	if tok1 == tok2 {
		t.Fatal("tokens should be unique")
	}
	if len(tok1) != 64 {
		t.Fatalf("token length: got %d, want 64", len(tok1))
	}
}

func TestNewAPIToken(t *testing.T) {
	tok1 := newAPIToken()
	tok2 := newAPIToken()
	if tok1 == "" || tok2 == "" {
		t.Fatal("tokens should not be empty")
	}
	if tok1 == tok2 {
		t.Fatal("tokens should be unique")
	}
	if !strings.HasPrefix(tok1, "sk-") {
		t.Fatalf("api token missing sk- prefix: %q", tok1)
	}
	// "sk-" (3) + 64 hex chars = 67
	if len(tok1) != 67 {
		t.Fatalf("api token length: got %d, want 67", len(tok1))
	}
}

func TestNowAdd(t *testing.T) {
	got := nowAdd(0)
	if got.IsZero() {
		t.Fatal("nowAdd(0) should not be zero")
	}
}

func TestSessionMiddleware_NoCookie(t *testing.T) {
	st := newTestApp(t)
	mw := SessionMiddleware(st)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect 303, got %d", rec.Code)
	}
}

func TestSessionMiddleware_BadCookie(t *testing.T) {
	st := newTestApp(t)
	mw := SessionMiddleware(st)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: "invalid"})
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect 303, got %d", rec.Code)
	}
}

func TestRequireRole_InsufficientRole(t *testing.T) {
	mw := RequireRole(model.RoleRoot)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect for nil user, got %d", rec.Code)
	}
}

func TestMethodOverride(t *testing.T) {
	called := false
	h := MethodOverride(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != "DELETE" {
			t.Fatalf("expected DELETE, got %s", r.Method)
		}
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/x?_method=DELETE", nil)
	h.ServeHTTP(rec, req)
	if !called {
		t.Fatal("handler was not called")
	}
}

func TestMethodOverride_NoOverride(t *testing.T) {
	h := MethodOverride(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Fatalf("expected POST, got %s", r.Method)
		}
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/x", nil)
	h.ServeHTTP(rec, req)
}

func TestWebAPIBridge_TriggerReloadNoop(t *testing.T) {
	b := &webAPIBridge{}
	if err := b.TriggerReload(); err != nil {
		t.Fatalf("TriggerReload with nil reloader: %v", err)
	}
}
