package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestLogger_JSONOutput(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, LevelDebug, FormatJSON)
	l.Info("hello", F("key", "value"), F("count", 42))

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("not valid JSON: %v (line=%q)", err, buf.String())
	}
	if rec["msg"] != "hello" {
		t.Errorf("msg: got %v", rec["msg"])
	}
	if rec["key"] != "value" {
		t.Errorf("key: got %v", rec["key"])
	}
	if rec["count"].(float64) != 42 {
		t.Errorf("count: got %v", rec["count"])
	}
	if rec["level"] != "info" {
		t.Errorf("level: got %v", rec["level"])
	}
	if _, ok := rec["ts"]; !ok {
		t.Error("missing ts")
	}
}

func TestLogger_TextOutput(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, LevelDebug, FormatText)
	l.Info("hello", F("k", "v"))
	if !strings.Contains(buf.String(), "level=info") {
		t.Errorf("missing level=info: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "k=v") {
		t.Errorf("missing k=v: %q", buf.String())
	}
}

func TestLogger_LevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, LevelWarn, FormatJSON)
	l.Debug("d")
	l.Info("i")
	l.Warn("w")
	l.Error("e")
	if buf.Len() == 0 {
		t.Fatal("no output")
	}
	out := buf.String()
	if strings.Contains(out, `"msg":"d"`) {
		t.Error("debug should be filtered")
	}
	if strings.Contains(out, `"msg":"i"`) {
		t.Error("info should be filtered")
	}
	if !strings.Contains(out, `"msg":"w"`) {
		t.Error("warn should be logged")
	}
	if !strings.Contains(out, `"msg":"e"`) {
		t.Error("error should be logged")
	}
}

func TestLogger_With(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, LevelDebug, FormatJSON).With(map[string]any{"service": "api", "version": 1})
	l.Info("test", F("k", "v"))

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if rec["service"] != "api" {
		t.Errorf("service: got %v", rec["service"])
	}
	if rec["version"].(float64) != 1 {
		t.Errorf("version: got %v", rec["version"])
	}
	if rec["k"] != "v" {
		t.Errorf("k: got %v", rec["k"])
	}
}

func TestLogger_WithField(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, LevelDebug, FormatJSON).WithField("request_id", "abc-123")
	l.Info("req")

	var rec map[string]any
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec)
	if rec["request_id"] != "abc-123" {
		t.Errorf("request_id: got %v", rec["request_id"])
	}
}

func TestLogger_SubLoggerDoesNotMutateParent(t *testing.T) {
	var buf bytes.Buffer
	parent := New(&buf, LevelDebug, FormatJSON)
	sub := parent.WithField("a", "1")
	sub.Info("from sub")

	// Parent should still have no fields.
	buf.Reset()
	parent.Info("from parent")
	out := buf.String()
	if strings.Contains(out, `"a":"1"`) {
		t.Errorf("parent should not inherit sub fields: %q", out)
	}
}

func TestF(t *testing.T) {
	f := F("k", "v")
	if f.Key != "k" || f.Value != "v" {
		t.Errorf("F() = %+v", f)
	}
}
