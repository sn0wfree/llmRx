//go:build windows

// Windows has no geteuid/setuid, no POSIX ownership and no scratch
// container story to support. The three helpers below are no-ops so the
// bootstrap sequence in main.go compiles unchanged; on Windows the
// gateway simply runs as whoever started it.
package main

// maybeChownDataDir is a no-op: Windows has no uid/gid ownership to fix.
func maybeChownDataDir(_, _ string) error { return nil }

// chownIfRoot is a no-op on Windows.
func chownIfRoot(_, _ string) error { return nil }

// dropPrivileges is a no-op on Windows: there is no equivalent of
// setuid, and a Windows process cannot shed its token this way.
func dropPrivileges(_ string) error { return nil }
