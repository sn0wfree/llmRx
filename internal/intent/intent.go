// Package intent wraps the L4 intent classifier implemented in
// Rust (internal/intent/rust). The Go side loads the cdylib at
// startup and calls its C ABI via cgo.
//
// Build:
//
//	make internal/intent/librust.so  (cargo build --release)
//	./llmRx  (uses the built .so)
//
// The package degrades gracefully: if the .so is missing, Classify
// returns Intent{Kind: "unknown", Score: 0} and the router skips
// L4. A startup warning is logged once.
package intent

import (
	"C"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"github.com/sn0wfree/llmRx/internal/logging"
)

// Intent is the parsed L4 result.
type Intent struct {
	Kind  string  `json:"kind"`
	Score float64 `json:"score"`
	Debug []Debug `json:"debug,omitempty"`
}

// Debug is one (label, weight) pair from the scorer.
type Debug struct {
	Label  string  `json:"label"`
	Weight float64 `json:"weight"`
}

// Classifier is the public interface.
type Classifier interface {
	Classify(text string) Intent
	Backend() string
	Close() error
}

// native wraps the cdylib.
//
// IMPORTANT: classify / backend / close are stored as
// unsafe.Pointer (the C function addresses from dlsym cast
// through the syscall layer) rather than as Go func values.
// Go 1.18 mis-compiles func-typed struct fields: every
// read becomes a bound-method dispatch (mov 0x20(field),%rdx;
// mov (%rdx),%rax; call *%rax) that dereferences a runtime.funcval
// wrapper the dlsym lookup never allocated. Storing them as
// unsafe.Pointer fields sidesteps the dispatch.
type native struct {
	mu       sync.Mutex
	cap      int
	so       unsafe.Pointer
	classify unsafe.Pointer
	backend  unsafe.Pointer
	close    unsafe.Pointer
}

// DefaultLibraryPath is the conventional location of the .so.
var DefaultLibraryPath = "internal/intent/rust/target/release/libllmrx_intent.so"

// Load attempts to dlopen the cdylib. The path is searched in this
// order:
//   1. The value of the LLMRX_INTENT_LIB env var
//   2. DefaultLibraryPath (relative to the binary's working dir)
//   3. The same path with "../" prepended (for binaries run from
//      the project root vs from the source tree)
func Load() (Classifier, error) {
	candidates := []string{}
	if v := os.Getenv("LLMRX_INTENT_LIB"); v != "" {
		candidates = append(candidates, v)
	}
	candidates = append(candidates,
		DefaultLibraryPath,
		"../"+DefaultLibraryPath,
		"/usr/local/lib/libllmrx_intent.so",
		"libllmrx_intent.so",
	)
	var lastErr error
	for _, p := range candidates {
		c, err := loadFrom(p)
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("intent: no classifier found (last err: %v)", lastErr)
}

// loadFrom opens a specific path.
func loadFrom(path string) (Classifier, error) {
	abs, _ := filepath.Abs(path)
	if _, err := os.Stat(abs); err != nil {
		return nil, err
	}
	handle, err := dlopen(abs)
	if err != nil {
		return nil, err
	}
	cs, err := loadClassify(handle)
	if err != nil {
		dlclose(handle)
		return nil, err
	}
	be, err := loadBackend(handle)
	if err != nil {
		dlclose(handle)
		return nil, err
	}
	cl, _ := loadClose(handle)
	n := &native{
		so:       handle,
		cap:      4096,
		classify: unsafe.Pointer(cs),
		backend:  unsafe.Pointer(be),
		close:    unsafe.Pointer(cl),
	}
	logging.Info("intent loaded native classifier",
		logging.F("path", abs),
	)
	return n, nil
}

// Nop is a no-op classifier used when the .so is unavailable.
type Nop struct{}

// Classify returns the "unknown" intent.
func (Nop) Classify(_ string) Intent { return Intent{Kind: "unknown"} }

// Backend reports the backend name.
func (Nop) Backend() string { return "disabled" }

// Close is a no-op.
func (Nop) Close() error { return nil }

func (n *native) Backend() string {
	if n.backend == nil {
		return "?"
	}
	cp := backendViaC(n.backend)
	if cp == nil {
		return "?"
	}
	// Read the C string up to NUL. The pointer came from
	// Rust's `*const c_char` which is *int8 (signed), so
	// dereference as int8 and only treat the value as a byte
	// once we've decided it's non-zero.
	p := (*int8)(unsafe.Pointer(cp))
	var buf [64]byte
	for i := 0; i < len(buf); i++ {
		c := *(*int8)(unsafe.Add(unsafe.Pointer(p), i))
		if c == 0 {
			return string(buf[:i])
		}
		buf[i] = byte(c)
	}
	return string(buf[:])
}

func (n *native) Classify(text string) Intent {
	if len(text) == 0 {
		return Intent{Kind: "unknown"}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.classify == nil {
		return Intent{Kind: "unknown"}
	}
	in := make([]byte, len(text)+1)
	copy(in, text)
	out := make([]byte, n.cap)
	written := classifyViaC(n.classify, (*C.char)(unsafe.Pointer(&in[0])), (*C.char)(unsafe.Pointer(&out[0])), int64(n.cap))
	if written < 0 {
		return Intent{Kind: "unknown"}
	}
	raw := out[:written]
	var res struct {
		Label string  `json:"label"`
		Score float64 `json:"score"`
		// The Rust side serialises debug as `Vec<(&str, f32)>`,
		// which serde renders as a JSON array of [label, weight]
		// tuples (not as objects). Decode into RawMessage first
		// so we can accept both shapes — Rust's current shape
		// (tuple array) and a future object-array shape — without
		// losing the structured Debug entries either way.
		Debug []json.RawMessage `json:"debug"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		logging.Warn("intent parse error",
			logging.F("error", err.Error()),
			logging.F("raw", strings.TrimRight(string(raw), "\x00")),
		)
		return Intent{Kind: "unknown"}
	}
	intent := Intent{Kind: res.Label, Score: res.Score}
	for _, d := range res.Debug {
		// Try object shape first: {"label": "...", "weight": N}.
		var obj struct {
			Label  string  `json:"label"`
			Weight float64 `json:"weight"`
		}
		if err := json.Unmarshal(d, &obj); err == nil {
			intent.Debug = append(intent.Debug, Debug{Label: obj.Label, Weight: obj.Weight})
			continue
		}
		// Fall back to Rust's tuple shape: [label, weight].
		var tup [2]any
		if err := json.Unmarshal(d, &tup); err == nil {
			label, _ := tup[0].(string)
			weight, _ := tup[1].(float64)
			intent.Debug = append(intent.Debug, Debug{Label: label, Weight: weight})
		}
	}
	return intent
}

func (n *native) Close() error {
	if n.so == nil {
		return nil
	}
	if n.close != nil {
		_ = closeViaC(n.close, n.so)
	}
	return dlclose(n.so)
}
