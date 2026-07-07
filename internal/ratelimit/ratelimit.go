// Package ratelimit provides a tiny in-memory token-bucket rate
// limiter keyed by token ID. It enforces RPM (requests per minute)
// and TPM (tokens per minute) ceilings without requiring an external
// dependency.
//
// State is process-local — distributed deployments should swap this
// out for a Redis-backed limiter.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter is a per-key sliding-window rate limiter. The window is
// exactly 60 seconds; entries older than that are evicted on the
// next Allow call for the same key.
type Limiter struct {
	mu    sync.Mutex
	state map[int64]*bucket
	now   func() time.Time // injected for tests
}

type bucket struct {
	// requests is a ring buffer of timestamps for the last minute.
	requests []time.Time
	// tokens is a parallel ring of token counts attributable to
	// each request (1 for "request itself", actual prompt+completion
	// added later).
	tokens []int
}

// New returns a fresh Limiter. now defaults to time.Now.
func New() *Limiter {
	return &Limiter{
		state: make(map[int64]*bucket),
		now:   time.Now,
	}
}

// Allow reports whether (key, rpm, tpm, promptTokens) is permitted
// under the configured ceilings. On success the (now, promptTokens)
// tuple is recorded; rpm/tpm of 0 mean "unlimited".
//
// Returns (allowed, reason). reason is empty when allowed.
func (l *Limiter) Allow(key int64, rpm, tpm int, promptTokens int) (bool, string) {
	if rpm == 0 && tpm == 0 {
		return true, ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-60 * time.Second)
	b, ok := l.state[key]
	if !ok {
		b = &bucket{}
		l.state[key] = b
	}
	// Evict expired entries.
	i := 0
	for ; i < len(b.requests); i++ {
		if b.requests[i].After(cutoff) {
			break
		}
	}
	if i > 0 {
		b.requests = b.requests[i:]
		b.tokens = b.tokens[i:]
	}
	if rpm > 0 && len(b.requests) >= rpm {
		return false, "rpm exceeded"
	}
	// Token budget is counted *after* the request — projected usage.
	projected := 0
	for _, n := range b.tokens {
		projected += n
	}
	if tpm > 0 && projected+promptTokens > tpm {
		return false, "tpm exceeded"
	}
	b.requests = append(b.requests, now)
	b.tokens = append(b.tokens, promptTokens)
	return true, ""
}

// Account records additional token usage against a key that has
// already been allowed. Used to bump TPM when the upstream's
// completion tokens come back. Safe to call with 0.
func (l *Limiter) Account(key int64, extraTokens int) {
	if extraTokens <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.state[key]
	if !ok {
		return
	}
	if len(b.tokens) > 0 {
		b.tokens[len(b.tokens)-1] += extraTokens
	}
}

// Reset clears all state. Useful for tests and admin "force reload".
func (l *Limiter) Reset() {
	l.mu.Lock()
	l.state = make(map[int64]*bucket)
	l.mu.Unlock()
}

// TrackedKeys returns the number of keys currently held (for /metrics
// and tests).
func (l *Limiter) TrackedKeys() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.state)
}

// SetNow overrides the wall clock. Tests only.
func (l *Limiter) SetNow(now func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.now = now
}