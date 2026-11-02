//go:build !cgo

// Builds without cgo cannot dlopen a cdylib at all, on any platform, so
// Load always fails and callers fall back to Nop.
//
// This is the only file defining Load for a no-cgo build: intent.go is
// '//go:build cgo', and dlopen_other.go is 'cgo && !linux && !darwin'.
// The three build sets are mutually exclusive, which is what keeps
// every symbol declared exactly once.
package intent

import (
	"errors"
	"unsafe"
)

func dlopen(path string) (unsafe.Pointer, error) {
	return nil, errors.New("intent: built without cgo, dlopen unavailable")
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

// Load reports that this build has no native classifier.
func Load() (Classifier, error) {
	return nil, errors.New("intent: native classifier requires cgo")
}

func classifyViaC(_, _, _ unsafe.Pointer, _ int64) int32 { return -1 }

func backendViaC(_ unsafe.Pointer) unsafe.Pointer { return nil }

func closeViaC(_, _ unsafe.Pointer) int32 { return -1 }
