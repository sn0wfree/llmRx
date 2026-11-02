//go:build !cgo || (!linux && !darwin)

// Stub dlopen for platforms without the cdylib path: any non-Linux/
// non-darwin OS, and every build with CGO_ENABLED=0 (the Windows and
// macOS release binaries). The classifier always reports "unknown"; the
// gateway runs normally with L4 intent classification disabled.
//
// The three *ViaC helpers exist because intent.go — which is also
// cgo-tagged — calls them once a symbol has been loaded. Their
// signatures must match dlopen_unix.go exactly; they take
// unsafe.Pointer rather than *C.char because this file cannot use cgo
// types.
package intent

import (
	"errors"
	"unsafe"
)

func dlopen(path string) (unsafe.Pointer, error) {
	return nil, errors.New("intent: dlopen not supported in this build")
}

func loadClassify(_ unsafe.Pointer) (unsafe.Pointer, error) {
	return nil, errors.New("intent: unavailable")
}
func loadBackend(_ unsafe.Pointer) (unsafe.Pointer, error) {
	return nil, errors.New("intent: unavailable")
}
func loadClose(_ unsafe.Pointer) (unsafe.Pointer, error) {
	return nil, nil
}
func dlclose(_ unsafe.Pointer) error { return nil }

// Load reports that no native classifier exists in this build. Callers
// treat the error as "fall back to Nop", which is exactly right: a
// CGO_ENABLED=0 binary has no way to dlopen a cdylib.
func Load() (Classifier, error) {
	return nil, errors.New("intent: native classifier requires cgo")
}

// The helpers below are unreachable: every load* returns an error, so no
// caller ever holds a resolved pointer. They exist only to satisfy the
// shared call sites.

func classifyViaC(_, _, _ unsafe.Pointer, _ int64) int32 { return -1 }

func backendViaC(_ unsafe.Pointer) unsafe.Pointer { return nil }

func closeViaC(_, _ unsafe.Pointer) int32 { return -1 }
