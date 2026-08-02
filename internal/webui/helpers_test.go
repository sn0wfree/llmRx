package webui

import (
	"path/filepath"
	"testing"

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

func TestWebAPIBridge_TriggerReloadNoop(t *testing.T) {
	b := &webAPIBridge{}
	if err := b.TriggerReload(); err != nil {
		t.Fatalf("TriggerReload with nil reloader: %v", err)
	}
}
