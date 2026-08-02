// Package ratelimit enforces per-token RPM (requests per minute)
// and TPM (tokens per minute) ceilings plus the plan-budget gate.
//
// The sliding-window state lives behind the Backend interface:
// MemoryBackend (process-local, the historical implementation) is
// the default and the fail-open fallback; PGWindowBackend (P12 M2)
// shares minute-bucket counters across every gateway replica via
// Postgres so limits are global, not per-node.
//
// The plan-budget gate stays in Limiter itself: used_usd lives in
// the database ledger, so the check is already global.
package ratelimit

import (
	"sync"
	"time"
)

// defaultShardCount is the number of shards used by MemoryBackend.
// 16 is empirically enough to keep the per-shard mutex under low
// contention at 10k+ QPS while keeping the per-shard map small
// enough that the linear scan inside Allow stays fast.
const defaultShardCount = 16

// bucket is a per-key sliding-window state used by MemoryBackend.
type bucket struct {
	requests []time.Time
	tokens   []int
}

// shard is one partition of the state map.
type shard struct {
	mu    sync.Mutex
	state map[int64]*bucket
}

// Limiter is a per-key rate limiter. It owns the plan-budget gate
// and delegates RPM/TPM window accounting to a Backend.
type Limiter struct {
	backend Backend
	now     func() time.Time
}

// New returns a Limiter backed by the process-local MemoryBackend
// (single-node behaviour, unchanged from before P12).
func New() *Limiter {
	return NewWithBackend(NewMemoryBackend())
}

// NewWithBackend returns a Limiter using the given window backend
// (e.g. NewPGWindowBackend for shared cluster counters).
func NewWithBackend(b Backend) *Limiter {
	return &Limiter{backend: b, now: time.Now}
}

// Backend returns the underlying window backend (metrics/tests).
func (l *Limiter) Backend() Backend { return l.backend }

// Allow reports whether (key, rpm, tpm, promptTokens) is permitted
// under the configured ceilings. On success the tuple is recorded.
// rpm/tpm of 0 mean "unlimited".
//
// budgetUSD/usedUSD/estimatedCostUSD form the plan-budget gate.
// budgetUSD == 0 means unlimited; otherwise the request is rejected
// when usedUSD + estimatedCostUSD would exceed budgetUSD. The check
// happens before rpm/tpm because hitting a billing stop is more
// actionable for the operator than a 429.
func (l *Limiter) Allow(key int64, rpm, tpm int, promptTokens int, budgetUSD, usedUSD, estimatedCostUSD float64) (bool, string) {
	if budgetUSD > 0 && usedUSD+estimatedCostUSD > budgetUSD {
		return false, "budget exceeded"
	}
	return l.backend.AllowWindow(key, rpm, tpm, promptTokens, l.now())
}

// Account records additional token usage against a key that has
// already been allowed. Used to bump TPM when the upstream's
// completion tokens come back. Safe to call with 0.
func (l *Limiter) Account(key int64, extraTokens int) {
	l.backend.AccountWindow(key, extraTokens, l.now())
}

// AccountRequest records one additional request against a key's RPM
// window without touching the TPM counter. Used for MCP tool calls,
// which count toward RPM but do not consume LLM tokens.
func (l *Limiter) AccountRequest(key int64) {
	l.backend.AccountRequestWindow(key, l.now())
}

// Reset clears all state. Useful for tests and admin "force reload".
func (l *Limiter) Reset() { l.backend.Reset() }

// TrackedKeys returns the total number of keys currently held
// (for /metrics and tests).
func (l *Limiter) TrackedKeys() int { return l.backend.TrackedKeys() }

// SetNow overrides the wall clock. Tests only.
func (l *Limiter) SetNow(now func() time.Time) { l.now = now }
