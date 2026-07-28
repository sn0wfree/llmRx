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

// Direct dispatchers so the Go side never has to invoke a
// bare C function pointer (Go 1.18 miscompiles unsafe.Pointer
// → func-value casts that come from a struct field).
static int call_classify(void* fn, const char* text, char* out, int64_t cap) {
    int (*f)(const char*, char*, long) = (int(*)(const char*, char*, long))fn;
    return f(text, out, cap);
}
static const char* call_backend(void* fn) {
    const char* (*f)(void) = (const char*(*)(void))fn;
    return f();
}
static int call_close(void* fn, void* handle) {
    int (*f)(void*) = (int(*)(void*))fn;
    return f(handle);
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// loadBackend / loadClassify / loadClose return the raw dlsym
// result as unsafe.Pointer. Calling these C functions from
// Go requires either a typed Go func value (which the 1.18
// compiler mis-handles when stored as a struct field) or a
// C wrapper that does the cast at the cgo boundary. We use
// the latter — see call_backend/call_classify/call_close
// above — so the runtime never tries to chase a function
// pointer through an interface dispatch.
func dlopen(path string) (unsafe.Pointer, error) {
	cs := C.CString(path)
	defer C.free(unsafe.Pointer(cs))
	h := C.llmrx_dlopen(cs)
	if h == nil {
		return nil, fmt.Errorf("dlopen %s: %s", path, C.GoString(C.llmrx_dlerror()))
	}
	return unsafe.Pointer(h), nil
}

func loadClassify(h unsafe.Pointer) (unsafe.Pointer, error) {
	cs := C.CString("llmrx_intent_classify")
	defer C.free(unsafe.Pointer(cs))
	sym := C.llmrx_dlsym(h, cs)
	if sym == nil {
		return nil, fmt.Errorf("dlsym classify: %s", C.GoString(C.llmrx_dlerror()))
	}
	return sym, nil
}

func loadBackend(h unsafe.Pointer) (unsafe.Pointer, error) {
	cs := C.CString("llmrx_intent_backend")
	defer C.free(unsafe.Pointer(cs))
	sym := C.llmrx_dlsym(h, cs)
	if sym == nil {
		return nil, fmt.Errorf("dlsym backend: %s", C.GoString(C.llmrx_dlerror()))
	}
	return sym, nil
}

func loadClose(h unsafe.Pointer) (unsafe.Pointer, error) {
	cs := C.CString("llmrx_intent_close")
	defer C.free(unsafe.Pointer(cs))
	sym := C.llmrx_dlsym(h, cs)
	if sym == nil {
		// close is optional; the Rust side doesn't actually expose
		// one yet. Return nil — the Close() helper treats a nil
		// close as a no-op stub.
		return nil, nil
	}
	return sym, nil
}

func dlclose(h unsafe.Pointer) error {
	if C.llmrx_dlclose(h) != 0 {
		return fmt.Errorf("dlclose: %s", C.GoString(C.llmrx_dlerror()))
	}
	return nil
}

// classifyViaC invokes the dlsym-resolved function pointer
// through a cgo wrapper, sidestepping the Go 1.18 struct-field
// function-pointer miscompile.
func classifyViaC(fn unsafe.Pointer, text *C.char, out *C.char, cap int64) int32 {
	return int32(C.call_classify(fn, text, out, C.long(cap)))
}

// backendViaC returns the C string pointer for the backend name.
func backendViaC(fn unsafe.Pointer) *C.char {
	return C.call_backend(fn)
}

// closeViaC invokes the optional close hook.
func closeViaC(fn unsafe.Pointer, handle unsafe.Pointer) int32 {
	return int32(C.call_close(fn, handle))
}
