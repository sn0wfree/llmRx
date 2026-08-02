package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// ──────────────────────────────────────────────────────────
// Level.String: covers the unknown-value branch (e.g. unexported
// sentinel produced by enum skew across versions).
// ──────────────────────────────────────────────────────────

func TestLevel_String_AllValues(t *testing.T) {
	tests := []struct {
		l    Level
		want string
	}{
		{LevelDebug, "debug"},
		{LevelInfo, "info"},
		{LevelWarn, "warn"},
		{LevelError, "error"},
		{Level(99), "unknown"},
		{Level(-1), "unknown"},
	}
	for _, tc := range tests {
		if got := tc.l.String(); got != tc.want {
			t.Errorf("Level(%d).String() = %q, want %q", tc.l, got, tc.want)
		}
	}
}

// ──────────────────────────────────────────────────────────
// Package-level shortcuts: Info / Warn / Error / Debug / With /
// WithRequestID. These all funnel through Default() so we install
// a custom default writer and assert against it.
// ──────────────────────────────────────────────────────────

// resetDefaultForTest installs a JSON-format default logger pointed
// at a private buffer and restores the original on cleanup. Tests
// that exercise package-level shortcuts call this first so they
// don't see leftovers from earlier tests (e.g. TestInit changing
// the format to text).
func resetDefaultForTest(t *testing.T) *bytes.Buffer {
	t.Helper()
	orig := Default()
	t.Cleanup(func() {
		Init(orig.level, orig.format)
		SetOutput(orig.out)
	})
	var buf bytes.Buffer
	Init(LevelDebug, FormatJSON)
	SetOutput(&buf)
	return &buf
}

func TestDefault_AndSetOutput(t *testing.T) {
	buf := resetDefaultForTest(t)

	l := Default()
	if l == nil {
		t.Fatal("Default() returned nil")
	}
	if l.out != buf {
		t.Fatal("Default().out should reflect SetOutput")
	}

	// Package-level shortcut writes through the default logger.
	Info("pkg-info", F("k", "v"))
	if !strings.Contains(buf.String(), `"msg":"pkg-info"`) {
		t.Errorf("Info() did not write: %q", buf.String())
	}
	if !strings.Contains(buf.String(), `"k":"v"`) {
		t.Errorf("Info() field missing: %q", buf.String())
	}
}

func TestPackageLevel_WarnErrorDebug(t *testing.T) {
	buf := resetDefaultForTest(t)

	Warn("w", F("k", "vw"))
	Error("e", F("k", "ve"))
	Debug("d", F("k", "vd"))

	out := buf.String()
	if !strings.Contains(out, `"msg":"w"`) || !strings.Contains(out, `"k":"vw"`) {
		t.Errorf("Warn() missing: %q", out)
	}
	if !strings.Contains(out, `"msg":"e"`) || !strings.Contains(out, `"k":"ve"`) {
		t.Errorf("Error() missing: %q", out)
	}
	if !strings.Contains(out, `"msg":"d"`) || !strings.Contains(out, `"k":"vd"`) {
		t.Errorf("Debug() missing: %q", out)
	}
}

func TestPackageLevel_Info_FilteredWhenAboveLevel(t *testing.T) {
	buf := resetDefaultForTest(t)
	SetLevel(LevelError) // only Error emits

	Info("should-not-emit")
	if buf.Len() != 0 {
		t.Fatalf("Info should be filtered at LevelError, got: %q", buf.String())
	}
	Error("should-emit")
	if !strings.Contains(buf.String(), `"msg":"should-emit"`) {
		t.Errorf("Error should emit at LevelError, got: %q", buf.String())
	}
}

func TestSetLevel(t *testing.T) {
	buf := resetDefaultForTest(t)
	SetLevel(LevelWarn)
	if Default().level != LevelWarn {
		t.Fatalf("SetLevel(LevelWarn): got %v", Default().level)
	}

	// Lower the threshold mid-flight and confirm previously-filtered
	// levels start emitting.
	SetLevel(LevelDebug)
	Info("now-emits")
	if !strings.Contains(buf.String(), `"msg":"now-emits"`) {
		t.Errorf("Info should emit after SetLevel(LevelDebug): %q", buf.String())
	}
}

func TestInit(t *testing.T) {
	orig := Default()
	t.Cleanup(func() {
		Init(orig.level, orig.format)
		SetOutput(orig.out)
	})

	// Init uses os.Stdout by design; we don't capture it. Instead,
	// verify it returns the new logger with the requested config.
	Init(LevelError, FormatText)
	l := Default()
	if l.level != LevelError {
		t.Errorf("Init level: got %v, want LevelError", l.level)
	}
	if l.format != FormatText {
		t.Errorf("Init format: got %v, want FormatText", l.format)
	}
}

// ──────────────────────────────────────────────────────────
// Package-level With / WithRequestID.
// ──────────────────────────────────────────────────────────

func TestPackage_With(t *testing.T) {
	buf := resetDefaultForTest(t)

	sub := With(map[string]any{"service": "gateway"})
	sub.Info("from-sub")

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if rec["service"] != "gateway" {
		t.Errorf("With fields not bound: got %v", rec["service"])
	}
	if rec["msg"] != "from-sub" {
		t.Errorf("msg: got %v", rec["msg"])
	}
}

func TestPackage_WithRequestID(t *testing.T) {
	buf := resetDefaultForTest(t)

	sub := WithRequestID("req-abc-123")
	sub.Info("req-test")

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if rec["request_id"] != "req-abc-123" {
		t.Errorf("request_id: got %v", rec["request_id"])
	}
}

// ──────────────────────────────────────────────────────────
// formatText: covers the "extra fields" branch that the basic
// TestLogger_TextOutput doesn't exercise (that test only emits
// one custom field; this emits several to exercise the sorted-key
// loop and ensure ts/level/msg don't leak into the extras list).
// ──────────────────────────────────────────────────────────

func TestFormatText_MultipleFields(t *testing.T) {
	l := New(&bytes.Buffer{}, LevelDebug, FormatText)
	// Use a known logger so the test is deterministic. Build the
	// record manually by going through the public API.
	var buf bytes.Buffer
	l = New(&buf, LevelDebug, FormatText)
	l.With(map[string]any{"svc": "gateway"}).
		WithField("request_id", "abc").
		Info("hello", F("count", 42))

	out := buf.String()
	// Stable prefix
	if !strings.HasPrefix(out, "20") {
		// ts starts with year (RFC3339Nano in UTC).
		t.Fatalf("expected RFC3339 timestamp prefix, got %q", out)
	}
	if !strings.Contains(out, "level=info") {
		t.Errorf("missing level=info: %q", out)
	}
	if !strings.Contains(out, `msg="hello"`) {
		t.Errorf("missing msg=\"hello\": %q", out)
	}
	if !strings.Contains(out, "svc=gateway") {
		t.Errorf("missing svc=gateway: %q", out)
	}
	if !strings.Contains(out, "request_id=abc") {
		t.Errorf("missing request_id=abc: %q", out)
	}
	if !strings.Contains(out, "count=42") {
		t.Errorf("missing count=42: %q", out)
	}
	// ts / level / msg must appear once each in the stable prefix
	// and never in the field list.
	if strings.Count(out, "ts=") != 0 {
		// The ts key is folded into the prefix via Sprintf, so
		// there should be no stray "ts=" field.
		t.Errorf("ts should not appear as a field: %q", out)
	}
}

// ──────────────────────────────────────────────────────────
// Concurrency: multiple goroutines writing through the same
// logger should not interleave records.
// ──────────────────────────────────────────────────────────

func TestLogger_ConcurrentWrites(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, LevelDebug, FormatJSON)

	const goroutines = 16
	const perGoroutine = 25
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				l.Info("msg", F("g", i), F("j", j))
			}
		}()
	}
	wg.Wait()

	// Every line must be valid JSON on its own. json.Encoder
	// appends a newline, so split on \n.
	total := strings.Count(buf.String(), "\n")
	if total != goroutines*perGoroutine {
		t.Fatalf("line count: got %d, want %d", total, goroutines*perGoroutine)
	}
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("interleaved JSON: %v (line=%q)", err, line)
		}
	}
}
