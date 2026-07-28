package intent

import (
	"os"
	"testing"
)

func TestNativeEndToEnd(t *testing.T) {
	// The cdylib lives at DefaultLibraryPath relative to the
	// project root, but `go test` runs with cwd set to the
	// package directory (internal/intent), so a simple
	// relative path doesn't resolve. Use LLMRX_INTENT_LIB
	// (the env var Load() honours first) to pin an absolute
	// path, then let the existing fallback chain take over
	// when the env var is empty.
	lib := ""
	for _, candidate := range []string{
		DefaultLibraryPath,
		"../" + DefaultLibraryPath,
		"../../" + DefaultLibraryPath,
	} {
		if fileExists(candidate) {
			lib = candidate
			break
		}
	}
	if lib == "" {
		t.Skipf("cdylib %s not built; run `make intent-rust`", DefaultLibraryPath)
	}
	t.Setenv("LLMRX_INTENT_LIB", lib)
	c, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer c.Close()
	if c.Backend() != "keyword" && c.Backend() != "onnx" {
		t.Fatalf("unexpected backend: %q", c.Backend())
	}
	cases := []struct {
		text string
		want string
	}{
		{"def hello():\n    return 42", "code"},
		{"Please summarise this article", "summary"},
		{"the quick brown fox", "general"},
		{"translate to french", "translate"},
	}
	for _, tc := range cases {
		got := c.Classify(tc.text)
		if got.Kind != tc.want {
			t.Errorf("Classify(%q) = %q, want %q", tc.text, got.Kind, tc.want)
		}
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
