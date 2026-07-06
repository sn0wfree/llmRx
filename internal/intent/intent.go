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
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"
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
type native struct {
	mu       sync.Mutex
	cap      int
	so       unsafe.Pointer
	classify classifyFn
	backend  backendFn
	close    closeFn
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
		classify: cs,
		backend:  be,
		close:    cl,
	}
	log.Printf("intent: loaded native classifier from %s", abs)
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
	p := n.backend()
	if p == nil {
		return "?"
	}
	// Read the C string up to NUL.
	var buf [64]byte
	for i := 0; i < len(buf); i++ {
		b := *(*byte)(unsafe.Add(unsafe.Pointer(p), i))
		if b == 0 {
			return string(buf[:i])
		}
		buf[i] = b
	}
	return string(buf[:])
}

func (n *native) Classify(text string) Intent {
	if len(text) == 0 {
		return Intent{Kind: "unknown"}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	in := append([]byte(text), 0)
	out := make([]byte, n.cap)
	written := n.classify(&in[0], &out[0], int64(n.cap))
	if written < 0 {
		return Intent{Kind: "unknown"}
	}
	var res struct {
		Label string  `json:"label"`
		Score float64 `json:"score"`
		Debug []struct {
			Label  string  `json:"label"`
			Weight float64 `json:"weight"`
		} `json:"debug"`
	}
	if err := json.Unmarshal(out[:written], &res); err != nil {
		log.Printf("intent: parse error: %v (raw=%s)", err, strings.TrimRight(string(out[:written]), "\x00"))
		return Intent{Kind: "unknown"}
	}
	intent := Intent{Kind: res.Label, Score: res.Score}
	for _, d := range res.Debug {
		intent.Debug = append(intent.Debug, Debug{Label: d.Label, Weight: d.Weight})
	}
	return intent
}

func (n *native) Close() error {
	if n.so == nil {
		return nil
	}
	if n.close != nil {
		n.close(n.so)
	}
	return dlclose(n.so)
}
