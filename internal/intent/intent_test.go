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
