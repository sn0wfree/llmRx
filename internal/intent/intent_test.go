package intent

import (
	"runtime"
	"testing"
)

func TestNopBackend(t *testing.T) {
	nop := Nop{}
	if nop.Backend() != "disabled" {
		t.Fatalf("Nop backend")
	}
	if nop.Classify("foo").Kind != "unknown" {
		t.Fatalf("Nop classify")
	}
}

func TestNopEmptyText(t *testing.T) {
	// Even if a classifier is loaded, empty text is short-circuited.
	n := &native{} // zero value; should not panic on empty text
	if got := n.Classify(""); got.Kind != "unknown" {
		t.Fatalf("empty: %+v", got)
	}
}

func TestNopClassifyVariousInputs(t *testing.T) {
	nop := Nop{}
	tests := []string{"", "hello", "what is the weather?", "a", "very long text " + string(make([]byte, 1000))}
	for _, text := range tests {
		t.Run(text[:min(len(text), 20)], func(t *testing.T) {
			intent := nop.Classify(text)
			if intent.Kind != "unknown" {
				t.Fatalf("Nop should return unknown, got %q", intent.Kind)
			}
			if intent.Score != 0 {
				t.Fatalf("Nop score should be 0, got %f", intent.Score)
			}
			if intent.Debug != nil {
				t.Fatalf("Nop debug should be nil, got %+v", intent.Debug)
			}
		})
	}
}

func TestNopClose(t *testing.T) {
	nop := Nop{}
	if err := nop.Close(); err != nil {
		t.Fatalf("Nop.Close: %v", err)
	}
	// Multiple closes should be safe
	if err := nop.Close(); err != nil {
		t.Fatalf("Nop.Close (second): %v", err)
	}
}

func TestNopSatisfiesInterface(t *testing.T) {
	var _ Classifier = Nop{}
	var _ Classifier = (*native)(nil)
}

func TestNativeCloseNilHandle(t *testing.T) {
	// native with nil handle should not panic
	n := &native{}
	if err := n.Close(); err != nil {
		t.Fatalf("Close with nil handle: %v", err)
	}
}

func TestNativeClassifyEmpty(t *testing.T) {
	// native with no classify function should not panic on empty text
	n := &native{}
	intent := n.Classify("")
	if intent.Kind != "unknown" {
		t.Fatalf("empty text: want unknown, got %q", intent.Kind)
	}
}

func TestLoadMissing(t *testing.T) {
	c, err := Load()
	if err != nil {
		// On a dev machine without the .so, Load() should fail
		// gracefully. We don't assert success here because the .so
		// may be present on CI; we only assert that Load() does not
		// panic.
		t.Logf("Load: %v (acceptable on this platform)", err)
		return
	}
	defer c.Close()
	if c.Backend() == "" {
		t.Fatal("backend should not be empty when loaded")
	}
	_ = runtime.GOOS
}

func TestLoadWithEnvVar(t *testing.T) {
	// Set env var to a non-existent path
	t.Setenv("LLMRX_INTENT_LIB", "/nonexistent/path/libllmrx_intent.so")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for non-existent library")
	}
}

func TestDefaultLibraryPath(t *testing.T) {
	if DefaultLibraryPath == "" {
		t.Fatal("DefaultLibraryPath should not be empty")
	}
}

func TestIntentStruct(t *testing.T) {
	intent := Intent{
		Kind:  "test",
		Score: 0.95,
		Debug: []Debug{{Label: "label1", Weight: 0.5}},
	}
	if intent.Kind != "test" {
		t.Fatalf("Kind: want test, got %q", intent.Kind)
	}
	if intent.Score != 0.95 {
		t.Fatalf("Score: want 0.95, got %f", intent.Score)
	}
	if len(intent.Debug) != 1 {
		t.Fatalf("Debug length: want 1, got %d", len(intent.Debug))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
