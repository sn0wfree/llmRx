package main

import (
	"strings"
	"testing"
)

func TestBuildVersion_LdflagsWins(t *testing.T) {
	old := version
	defer func() { version = old }()

	version = "v1.2.3"
	if got := buildVersion(); got != "v1.2.3" {
		t.Fatalf("buildVersion() = %q, want v1.2.3", got)
	}
}

func TestBuildVersion_FallsBackToDevInTestBinary(t *testing.T) {
	old := version
	defer func() { version = old }()

	// A test binary has no module version and (usually) no vcs stamp,
	// so the fallback must be a usable string rather than empty.
	version = "dev"
	if got := buildVersion(); got == "" {
		t.Fatal("buildVersion() returned empty string")
	}
}

func TestBuildCommit_PrefersLdflagsAndShortens(t *testing.T) {
	old := commit
	defer func() { commit = old }()

	commit = "0123456789abcdef0123456789abcdef01234567"
	if got := buildCommit(); got != "0123456" {
		t.Fatalf("buildCommit() = %q, want the 7-char prefix", got)
	}
}

func TestBuildCommit_ShortValueUnchanged(t *testing.T) {
	old := commit
	defer func() { commit = old }()

	commit = "abc123"
	if got := buildCommit(); got != "abc123" {
		t.Fatalf("buildCommit() = %q, want abc123", got)
	}
}

func TestBuildDate_PrefersLdflags(t *testing.T) {
	old := date
	defer func() { date = old }()

	date = "2026-11-01T10:00:00Z"
	if got := buildDate(); got != "2026-11-01T10:00:00Z" {
		t.Fatalf("buildDate() = %q", got)
	}
}

// The -version output is the one thing operators paste into bug
// reports, so it must always carry a version and the Go toolchain.
func TestBuildInfoString(t *testing.T) {
	oldV, oldC, oldD := version, commit, date
	defer func() { version, commit, date = oldV, oldC, oldD }()

	version, commit, date = "v0.1.0", "abc1234", "2026-11-01T10:00:00Z"
	got := buildInfoString()

	for _, want := range []string{"v0.1.0", "abc1234", "2026-11-01T10:00:00Z", "go1."} {
		if !strings.Contains(got, want) {
			t.Fatalf("buildInfoString() = %q, missing %q", got, want)
		}
	}
}

func TestBuildInfoString_NeverEmptyTokens(t *testing.T) {
	oldV, oldC, oldD := version, commit, date
	defer func() { version, commit, date = oldV, oldC, oldD }()

	// Degenerate ldflags values must not render as blanks.
	version, commit, date = "", "", ""
	got := buildInfoString()
	if strings.Contains(got, "()") {
		t.Fatalf("buildInfoString() = %q, commit/date rendered empty", got)
	}
	for _, tok := range strings.Fields(got) {
		if tok == "(" || tok == ")" || tok == "," {
			continue
		}
	}
}

func TestShortCommit(t *testing.T) {
	cases := map[string]string{
		"0123456789abcdef": "0123456",
		"abc":              "abc",
		"":                 "",
		"abcdefg":          "abcdefg",
	}
	for in, want := range cases {
		if got := shortCommit(in); got != want {
			t.Fatalf("shortCommit(%q) = %q, want %q", in, got, want)
		}
	}
}
