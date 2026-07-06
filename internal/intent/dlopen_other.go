//go:build !linux && !darwin

// Stub dlopen for unsupported platforms. The classifier will
// always return "unknown".
package intent

import (
	"errors"
	"unsafe"
)

type classifyFn func(text *byte, out *byte, cap int64) int32
type backendFn func() *byte
type closeFn func(handle unsafe.Pointer) int32

func dlopen(path string) (unsafe.Pointer, error) {
	return nil, errors.New("intent: dlopen not supported on this platform")
}

func loadClassify(_ unsafe.Pointer) (classifyFn, error) {
	return nil, errors.New("intent: unavailable")
}
func loadBackend(_ unsafe.Pointer) (backendFn, error) {
	return nil, errors.New("intent: unavailable")
}
func loadClose(_ unsafe.Pointer) (closeFn, error) {
	return func(_ unsafe.Pointer) int32 { return 0 }, nil
}
func dlclose(_ unsafe.Pointer) error { return nil }
