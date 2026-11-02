// Package intent wraps the L4 intent classifier implemented in Rust
// (internal/intent/rust).
//
// This file holds the platform-independent surface: the public types,
// the interface and the no-op classifier. Everything that touches cgo
// lives in intent.go (build-tagged), so a CGO_ENABLED=0 build — the
// Windows and macOS release binaries — still gets the complete API and
// simply falls back to Nop.
package intent

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

// DefaultLibraryPath is the conventional location of the .so.
var DefaultLibraryPath = "internal/intent/rust/target/release/libllmrx_intent.so"

// Nop is the classifier used when the cdylib is unavailable — including
// every build without cgo. It reports "unknown" so the router skips L4
// and continues with the other routing layers.
type Nop struct{}

// Classify always reports an unknown intent.
func (Nop) Classify(_ string) Intent { return Intent{Kind: "unknown"} }

// Backend reports the backend name.
func (Nop) Backend() string { return "disabled" }

// Close is a no-op.
func (Nop) Close() error { return nil }
