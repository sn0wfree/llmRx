package runtime

import (
	"bytes"
	"strings"
	"testing"
)

func TestLevelFilter_DropsBelowThreshold(t *testing.T) {
	var out bytes.Buffer
	rt := New()
	rt.SetLogLevel(2) // warn: drops info and debug

	f := NewLevelFilter(rt, &out)
	f.Write([]byte("debug: this should drop\n"))
	f.Write([]byte("info: this should drop\n"))
	f.Write([]byte("warn: this stays\n"))
	f.Write([]byte("error: this stays\n"))
	f.Write([]byte("plain info line (no prefix) drops\n"))

	got := out.String()
	if !strings.Contains(got, "warn: this stays") {
		t.Errorf("warn should pass: %q", got)
	}
	if !strings.Contains(got, "error: this stays") {
		t.Errorf("error should pass: %q", got)
	}
	if strings.Contains(got, "this should drop") {
		t.Errorf("debug/info should be dropped: %q", got)
	}
	if strings.Contains(got, "plain info line") {
		t.Errorf("unprefixed line should drop at warn level: %q", got)
	}
}

func TestLevelFilter_PassesAllAtDebug(t *testing.T) {
	var out bytes.Buffer
	rt := New()
	rt.SetLogLevel(0) // debug: everything passes

	f := NewLevelFilter(rt, &out)
	f.Write([]byte("debug: d\n"))
	f.Write([]byte("info: i\n"))
	f.Write([]byte("warn: w\n"))
	f.Write([]byte("error: e\n"))

	got := out.String()
	for _, want := range []string{"debug: d", "info: i", "warn: w", "error: e"} {
		if !strings.Contains(got, want) {
			t.Errorf("debug level should pass %q: %q", want, got)
		}
	}
}

func TestLevelFilter_OnlyErrorAtError(t *testing.T) {
	var out bytes.Buffer
	rt := New()
	rt.SetLogLevel(3) // error: drops everything below

	f := NewLevelFilter(rt, &out)
	f.Write([]byte("debug: d\n"))
	f.Write([]byte("info: i\n"))
	f.Write([]byte("warn: w\n"))
	f.Write([]byte("error: e\n"))

	got := out.String()
	if !strings.Contains(got, "error: e") {
		t.Errorf("error should pass: %q", got)
	}
	if strings.Contains(got, "info: i") {
		t.Errorf("info should be dropped at error level: %q", got)
	}
}

func TestLevelFilter_ThresholdLiveUpdate(t *testing.T) {
	// Filter reads from rt on every Write. Lower the threshold
	// between writes and verify the next write respects the new
	// value.
	var out bytes.Buffer
	rt := New()
	rt.SetLogLevel(3) // error

	f := NewLevelFilter(rt, &out)
	f.Write([]byte("info: dropped first\n"))
	if strings.Contains(out.String(), "dropped first") {
		t.Fatalf("info should drop at error level: %q", out.String())
	}

	rt.SetLogLevel(0) // now debug — everything passes
	f.Write([]byte("info: passes second\n"))
	if !strings.Contains(out.String(), "passes second") {
		t.Fatalf("info should pass after lowering: %q", out.String())
	}
}

func TestLevelForLine(t *testing.T) {
	cases := map[string]int64{
		"debug: foo":        0,
		"info: bar":         1,
		"warn: baz":         2,
		"error: qux":        3,
		"plain line":        1,
		"warning: typo":     1, // "warning:" not recognised
		"alert: foo":        1,
		"":                  1,
		"warn: ":            2,
		"warning: ":         1,
	}
	for s, want := range cases {
		if got := levelForLine(s); got != want {
			t.Errorf("levelForLine(%q): got %d, want %d", s, got, want)
		}
	}
}
