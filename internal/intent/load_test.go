package intent

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

func TestLoadFrom_NonExistentPath(t *testing.T) {
	_, err := loadFrom("/nonexistent/path/libtest.so")
	if err == nil {
		t.Fatal("expected error for non-existent path")
	}
}

func TestLoadFrom_InvalidFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "fake.so")
	if err := os.WriteFile(p, []byte("not a shared library"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadFrom(p)
	if err == nil {
		t.Fatal("expected error for invalid .so file")
	}
}

func TestLoadFrom_RelativePathResolved(t *testing.T) {
	_, err := loadFrom("nonexistent_relative.so")
	if err == nil {
		t.Fatal("expected error for non-existent relative path")
	}
}

func TestNative_BackendNilFunc(t *testing.T) {
	n := &native{}
	if got := n.Backend(); got != "?" {
		t.Fatalf("nil backend should return '?', got %q", got)
	}
}

func TestNative_BackendNilPointer(t *testing.T) {
	n := &native{
		backend: func() *byte { return nil },
	}
	if got := n.Backend(); got != "?" {
		t.Fatalf("nil pointer backend should return '?', got %q", got)
	}
}

func TestNative_CloseWithNilCloseFunc(t *testing.T) {
	n := &native{
		so:    unsafe.Pointer(nil),
		close: nil,
	}
	if err := n.Close(); err != nil {
		t.Fatalf("Close with nil handle and nil close func should not error: %v", err)
	}
}

func TestLoad_MultipleCandidatesExhausted(t *testing.T) {
	t.Setenv("LLMRX_INTENT_LIB", "/nonexistent/a.so")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error when all candidates fail")
	}
}

func TestNop_ClassifyNilText(t *testing.T) {
	nop := Nop{}
	got := nop.Classify("")
	if got.Kind != "unknown" {
		t.Fatalf("empty text: want unknown, got %q", got.Kind)
	}
}

func TestNop_InterfaceCompliance(t *testing.T) {
	var c Classifier = Nop{}
	_ = c.Classify("test")
	_ = c.Backend()
	_ = c.Close()
}
