//go:build !linux && !darwin

// Stub dlopen for platforms without the cdylib path (Windows, BSD, ...).
// The classifier always reports "unknown"; the gateway runs normally
// with L4 intent classification disabled.
//
// This file is selected purely by OS, NOT by cgo: intent.go is
// '//go:build cgo' and provides Load()/loadFrom() plus the native type.
// Gating this file on !cgo as well made both files compile on
// windows+cgo, which produced "Load redeclared".
package intent

import (
	"errors"
	"unsafe"
)

func dlopen(path string) (unsafe.Pointer, error) {
	return nil, errors.New("intent: dlopen not supported on this platform")
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

// Load reports that no native classifier exists on this platform.
// Callers treat the error as "fall back to Nop", which is exactly right.
func Load() (Classifier, error) {
	return nil, errors.New("intent: native classifier unsupported on this platform")
}

// The helpers below are unreachable: every load* returns an error, so no
// caller ever holds a resolved pointer. They exist only to satisfy the
// shared call sites, and their signatures must match dlopen_unix.go
// exactly (unsafe.Pointer, not *C.char — this file cannot use cgo
// types).

func classifyViaC(_, _, _ unsafe.Pointer, _ int64) int32 { return -1 }

func backendViaC(_ unsafe.Pointer) unsafe.Pointer { return nil }

func closeViaC(_, _ unsafe.Pointer) int32 { return -1 }
