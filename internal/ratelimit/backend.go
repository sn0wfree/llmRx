package ratelimit

import "time"

// Backend owns the RPM/TPM sliding-window state for a Limiter.
// The budget gate stays in Limiter.Allow (it reads DB-ledger
// values that are already global); only the window counters are
// backend-specific.
//
// Implementations:
//   - MemoryBackend: process-local exact 60s sliding window
//     (single-node default, current behaviour).
//   - PGWindowBackend: Postgres minute-bucket counters shared by
//     every replica (P12 M2). Atomicity via a FOR UPDATE read +
//     upsert inside a transaction; fails open to a local window
//     when the database is unreachable (see NewPGWindowBackend).
type Backend interface {
	// AllowWindow reports whether (key, rpm, tpm, promptTokens) fits
	// in the key's current window and records it on success. rpm/tpm
	// of 0 mean unlimited. now is the window reference time.
	AllowWindow(key int64, rpm, tpm int, promptTokens int, now time.Time) (ok bool, reason string)

	// AccountWindow credits additional tokens (upstream completion
	// tokens) to the key's most recent window. No-op for 0.
	AccountWindow(key int64, extraTokens int, now time.Time)

	// AccountRequestWindow records one request with zero tokens
	// (MCP tool calls count toward RPM only).
	AccountRequestWindow(key int64, now time.Time)

	// Reset clears all window state (tests / admin force reload).
	Reset()

	// TrackedKeys returns the number of keys with window state.
	TrackedKeys() int
}
