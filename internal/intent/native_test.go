package intent

import (
	"os"
	"testing"
)

func TestNativeEndToEnd(t *testing.T) {
	lib := DefaultLibraryPath
	if !fileExists(lib) && !fileExists("../"+lib) {
		t.Skipf("cdylib %s not built; run `make intent-rust`", lib)
	}
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
