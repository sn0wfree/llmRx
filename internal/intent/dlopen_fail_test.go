package intent

import (
	"os"
	"testing"
)

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

// dlopenFailures covers the sym==nil branches in loadClassify /
// loadBackend / loadClose. We trigger them by loading a real
// .so (e.g. the libgcc that dlopen_unix.go already requires via
// -ldl) and asking for symbols that don't exist in it.
//
// Skip on the other-platform build where dlopen is a stub.

func TestLoadFrom_DlopenSuccessButMissingSymbols(t *testing.T) {
	// Use libc.so as the "library": it always exists on Linux,
	// but it doesn't export our Rust ABI symbols, so all three
	// loadXxx calls must return errors.
	const libc = "libc.so.6"
	if !fileExists(libc) && !fileExists("/lib/x86_64-linux-gnu/"+libc) {
		t.Skipf("libc not found at expected paths")
	}
	_, err := loadFrom(libc)
	if err == nil {
		t.Fatal("expected loadFrom to fail: libc has no llmrx_intent_* symbols")
	}
}

func TestLoadFrom_DlopenInvalidPath(t *testing.T) {
	// Path that doesn't exist.
	_, err := loadFrom("/nonexistent/path/libtest.so")
	if err == nil {
		t.Fatal("expected error for non-existent path")
	}
}

func TestLoadFrom_DlopenInvalidFile(t *testing.T) {
	// Path that exists but is not a valid ELF.
	tmp := t.TempDir()
	path := tmp + "/fake.so"
	if err := writeFile(path, []byte("not a shared library")); err != nil {
		t.Fatal(err)
	}
	_, err := loadFrom(path)
	if err == nil {
		t.Fatal("expected error for invalid .so")
	}
}
