package intent

import (
	"testing"
)

func TestNop_Classify(t *testing.T) {
	n := Nop{}
	r := n.Classify("hello world")
	if r.Kind != "unknown" {
		t.Errorf("kind=%q want unknown", r.Kind)
	}
	if r.Score != 0 {
		t.Errorf("score=%v want 0", r.Score)
	}
}

func TestNop_ClassifyEmpty(t *testing.T) {
	n := Nop{}
	r := n.Classify("")
	if r.Kind != "unknown" {
		t.Errorf("kind=%q want unknown", r.Kind)
	}
}

func TestNop_Backend(t *testing.T) {
	n := Nop{}
	if got := n.Backend(); got != "disabled" {
		t.Errorf("backend=%q want disabled", got)
	}
}

func TestNop_Close(t *testing.T) {
	n := Nop{}
	if err := n.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

func TestNative_Backend_NilFunc(t *testing.T) {
	n := &native{}
	if got := n.Backend(); got != "?" {
		t.Errorf("backend with nil func = %q want ?", got)
	}
}

func TestNative_Close_NilSo(t *testing.T) {
	n := &native{so: nil}
	if err := n.Close(); err != nil {
		t.Errorf("close with nil so: %v", err)
	}
}

func TestNative_Classify_EmptyText(t *testing.T) {
	n := &native{}
	r := n.Classify("")
	if r.Kind != "unknown" {
		t.Errorf("kind=%q want unknown", r.Kind)
	}
}

func TestLoad_NoLibrariesFound(t *testing.T) {
	_, err := Load()
	if err == nil {
		t.Fatal("expected error when no .so found")
	}
}

func TestLoadFrom_NonexistentPath(t *testing.T) {
	_, err := loadFrom("/nonexistent/path/to/lib.so")
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

func TestIntent_Struct(t *testing.T) {
	i := Intent{Kind: "coding", Score: 0.95, Debug: []Debug{{Label: "rust", Weight: 0.8}}}
	if i.Kind != "coding" {
		t.Errorf("kind=%q", i.Kind)
	}
	if len(i.Debug) != 1 {
		t.Errorf("debug len=%d", len(i.Debug))
	}
}
