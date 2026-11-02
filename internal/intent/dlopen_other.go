//go:build cgo && !linux && !darwin

// Stub dlopen for cgo builds on platforms without a dlopen path
// (Windows). The classifier always reports "unknown"; the gateway runs
// normally with L4 intent classification disabled.
//
// The tag includes `cgo` deliberately: intent.go (also `cgo`) defines
// Load/loadFrom, so a non-cgo build must not include this file or Load
// is declared twice. Non-cgo builds are covered by dlopen_nocgo.go.
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

// The helpers below are unreachable: every load* returns an error, so no
// caller ever holds a resolved pointer. They exist only to satisfy the
// shared call sites, and their signatures must match dlopen_unix.go
// exactly (unsafe.Pointer, not *C.char — this file cannot use cgo
// types).

func classifyViaC(_, _, _ unsafe.Pointer, _ int64) int32 { return -1 }

func backendViaC(_ unsafe.Pointer) unsafe.Pointer { return nil }

func closeViaC(_, _ unsafe.Pointer) int32 { return -1 }
