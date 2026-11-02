//go:build !cgo && (linux || darwin)

// Linux/macOS built without cgo: the dlopen path is impossible, so Load
// always falls back to Nop. dlopen_unix.go is excluded (it needs cgo)
// and dlopen_other.go is excluded (it is for other OSes), so the stub
// helpers live here.
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
