//go:build linux || darwin

// Real dlopen implementation for Linux and macOS.
package intent

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdlib.h>

static void* llmrx_dlopen(const char* path) { return dlopen(path, RTLD_NOW | RTLD_LOCAL); }
static void* llmrx_dlsym(void* h, const char* name) { return dlsym(h, name); }
static int llmrx_dlclose(void* h) { return dlclose(h); }
static const char* llmrx_dlerror(void) { return dlerror(); }
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// Function pointer types matching the Rust C ABI.
type classifyFn func(text *byte, out *byte, cap int64) int32
type backendFn func() *byte
type closeFn func(handle unsafe.Pointer) int32

func dlopen(path string) (unsafe.Pointer, error) {
	cs := C.CString(path)
	defer C.free(unsafe.Pointer(cs))
	h := C.llmrx_dlopen(cs)
	if h == nil {
		return nil, fmt.Errorf("dlopen %s: %s", path, C.GoString(C.llmrx_dlerror()))
	}
	return unsafe.Pointer(h), nil
}

func loadClassify(h unsafe.Pointer) (classifyFn, error) {
	cs := C.CString("llmrx_intent_classify")
	defer C.free(unsafe.Pointer(cs))
	sym := C.llmrx_dlsym(h, cs)
	if sym == nil {
		return nil, fmt.Errorf("dlsym classify: %s", C.GoString(C.llmrx_dlerror()))
	}
	return *(*classifyFn)(unsafe.Pointer(&sym)), nil
}

func loadBackend(h unsafe.Pointer) (backendFn, error) {
	cs := C.CString("llmrx_intent_backend")
	defer C.free(unsafe.Pointer(cs))
	sym := C.llmrx_dlsym(h, cs)
	if sym == nil {
		return nil, fmt.Errorf("dlsym backend: %s", C.GoString(C.llmrx_dlerror()))
	}
	return *(*backendFn)(unsafe.Pointer(&sym)), nil
}

func loadClose(h unsafe.Pointer) (closeFn, error) {
	cs := C.CString("llmrx_intent_close")
	defer C.free(unsafe.Pointer(cs))
	sym := C.llmrx_dlsym(h, cs)
	if sym == nil {
		// close is optional; the Rust side doesn't actually expose
		// one yet. Return a stub that succeeds.
		return func(_ unsafe.Pointer) int32 { return 0 }, nil
	}
	return *(*closeFn)(unsafe.Pointer(&sym)), nil
}

func dlclose(h unsafe.Pointer) error {
	if C.llmrx_dlclose(h) != 0 {
		return fmt.Errorf("dlclose: %s", C.GoString(C.llmrx_dlerror()))
	}
	return nil
}
