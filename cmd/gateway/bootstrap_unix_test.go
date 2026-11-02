//go:build !windows

// Unix-only bootstrap tests: the chown helpers and privilege drop have
// no Windows equivalent (bootstrap_windows.go stubs them as no-ops), so
// these live in their own file rather than in main_test.go where they
// broke `go vet` for the windows target.
package main

import "testing"

func TestChownRecursive_NonRootNoop(t *testing.T) {
	dir := t.TempDir()
	if err := chownRecursive(dir, 1000, 1000); err != nil {
		t.Errorf("chownRecursive as non-root should not error: %v", err)
	}
}

func TestChownIfRoot_NonRootNoop(t *testing.T) {
	if err := chownIfRoot("/tmp/test", "nobody"); err != nil {
		t.Errorf("chownIfRoot as non-root should not error: %v", err)
	}
}

func TestDropPrivileges_NonRootNoop(t *testing.T) {
	if err := dropPrivileges("nobody"); err != nil {
		t.Errorf("dropPrivileges as non-root should not error: %v", err)
	}
}
