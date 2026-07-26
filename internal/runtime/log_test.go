package runtime

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestLogLevelName(t *testing.T) {
	tests := []struct {
		level int64
		want  string
	}{
		{0, "debug"},
		{1, "info"},
		{2, "warn"},
		{3, "error"},
	}
	for _, tc := range tests {
		got := LogLevelName(tc.level)
		if got != tc.want {
			t.Errorf("LogLevelName(%d): got %q, want %q", tc.level, got, tc.want)
		}
	}
}

func TestLogLevelName_OutOfRange(t *testing.T) {
	got := LogLevelName(5)
	if got == "" {
		t.Fatal("should not be empty for unknown level")
	}
	if !strings.Contains(got, "?") {
		t.Fatalf("unknown level should contain '?': got %q", got)
	}
}

func TestNewLevelFilter(t *testing.T) {
	rt := New()
	var buf bytes.Buffer
	f := NewLevelFilter(rt, &buf)

	rt.SetLogLevel(1)

	input := []byte("info: test message\n")
	n, err := f.Write(input)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(input) {
		t.Fatalf("Write returned %d, want %d", n, len(input))
	}
	if !strings.Contains(buf.String(), "test message") {
		t.Fatalf("output should contain message: %s", buf.String())
	}
}

func TestLevelFilter_FiltersBelowThreshold(t *testing.T) {
	rt := New()
	var buf bytes.Buffer
	f := NewLevelFilter(rt, &buf)

	rt.SetLogLevel(3)

	input := []byte("info: should be filtered\n")
	n, _ := f.Write(input)
	if n != len(input) {
		t.Fatalf("Write should report full length, got %d want %d", n, len(input))
	}
	if buf.Len() != 0 {
		t.Fatalf("info should be filtered at error level, got: %s", buf.String())
	}
}

func TestLevelFilter_PassesAtThreshold(t *testing.T) {
	rt := New()
	var buf bytes.Buffer
	f := NewLevelFilter(rt, &buf)

	rt.SetLogLevel(2)

	f.Write([]byte("warn: warning message\n"))
	if !strings.Contains(buf.String(), "warning message") {
		t.Fatalf("warn should pass at warn level: %s", buf.String())
	}
}

func TestLevelFilter_PassesAboveThreshold(t *testing.T) {
	rt := New()
	var buf bytes.Buffer
	f := NewLevelFilter(rt, &buf)

	rt.SetLogLevel(0)

	f.Write([]byte("error: error message\n"))
	if !strings.Contains(buf.String(), "error message") {
		t.Fatalf("error should pass at debug level: %s", buf.String())
	}
}

func TestLevelFilter_MultipleLines(t *testing.T) {
	rt := New()
	var buf bytes.Buffer
	f := NewLevelFilter(rt, &buf)

	rt.SetLogLevel(2)
	f.Write([]byte("info: filtered\nwarn: kept\nerror: kept\n"))
	out := buf.String()
	if strings.Contains(out, "filtered") {
		t.Fatalf("info should be filtered: %s", out)
	}
	if !strings.Contains(out, "kept") {
		t.Fatalf("warn/error should be kept: %s", out)
	}
}

func TestInstallLogFilter(t *testing.T) {
	rt := New()
	var buf bytes.Buffer
	InstallLogFilter(rt, &buf)
	rt.SetLogLevel(0)

	log.Printf("debug: test debug message")
	if !strings.Contains(buf.String(), "test debug message") {
		t.Fatalf("debug message should pass: %s", buf.String())
	}
}
