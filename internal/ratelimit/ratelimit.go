// Package ratelimit provides a tiny in-memory token-bucket rate
// limiter keyed by token ID. It enforces RPM (requests per minute)
// and TPM (tokens per minute) ceilings without requiring an external
// dependency.
//
// The state map is sharded by `key % shardCount` so the single
// global Mutex from earlier revisions does not serialize all
// in-flight rate-limit decisions. Multi-tenant QPS scales roughly
// linearly with shardCount (16 by default).
//
// State is process-local — distributed deployments should swap this
// out for a Redis-backed limiter.
package ratelimit

import (
	"sync"
	"sync/atomic"
	"time"
)

// defaultShardCount is the number of shards used by Limiter to
// distribute contention. 16 is empirically enough to keep the
// per-shard mutex under low contention at 10k+ QPS while keeping the
// per-shard map small enough that the linear scan inside Allow()
// stays fast.
const defaultShardCount = 16

// bucket is a per-key sliding-window state. Requests/tokens are
// appended on Allow() and trimmed on the next Allow() to keep the
// slice bounded by rpm (60-second window).
type bucket struct {
	requests []time.Time
	tokens   []int
}

// shard is one partition of the state map.
type shard struct {
	mu    sync.Mutex
	state map[int64]*bucket
}

// Limiter is a per-key sliding-window rate limiter. The window is
// exactly 60 seconds; entries older than that are evicted on the
// next Allow call for the same key.
type Limiter struct {
	shards []shard
	now    atomic.Value // holds func() time.Time
}

// New returns a fresh Limiter. now defaults to time.Now.
func New() *Limiter {
	l := &Limiter{
		shards: make([]shard, defaultShardCount),
	}
	l.now.Store(func() time.Time { return time.Now() })
	for i := range l.shards {
		l.shards[i].state = make(map[int64]*bucket)
	}
	return l
}

// pickShard hashes a key into one of the shards. We use FNV-like
// mixing so keys that are sequential IDs (the common case for
// token_id auto-increments) spread evenly across shards instead
// of clumping onto a few.
func (l *Limiter) pickShard(key int64) *shard {
	// Knuth multiplicative hash, then mod shards.
	h := uint64(key) * 11400714819323198485
	return &l.shards[h%uint64(len(l.shards))]
}

// Allow reports whether (key, rpm, tpm, promptTokens) is permitted
// under the configured ceilings. On success the (now, promptTokens)
// tuple is recorded; rpm/tpm of 0 mean "unlimited".
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
	if rpm == 0 && tpm == 0 {
		return true, ""
	}
	s := l.pickShard(key)
	nowFn := l.now.Load().(func() time.Time)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowFn()
	cutoff := now.Add(-60 * time.Second)
	b, ok := s.state[key]
	if !ok {
		b = &bucket{}
		s.state[key] = b
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
	s := l.pickShard(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.state[key]
	if !ok {
		return
	}
	if len(b.tokens) > 0 {
		b.tokens[len(b.tokens)-1] += extraTokens
	}
}

// Reset clears all state. Useful for tests and admin "force reload".
func (l *Limiter) Reset() {
	for i := range l.shards {
		l.shards[i].mu.Lock()
		l.shards[i].state = make(map[int64]*bucket)
		l.shards[i].mu.Unlock()
	}
}

// TrackedKeys returns the total number of keys currently held
// across all shards (for /metrics and tests).
func (l *Limiter) TrackedKeys() int {
	total := 0
	for i := range l.shards {
		l.shards[i].mu.Lock()
		total += len(l.shards[i].state)
		l.shards[i].mu.Unlock()
	}
	return total
}

// SetNow overrides the wall clock. Tests only.
func (l *Limiter) SetNow(now func() time.Time) {
	l.now.Store(now)
}