package ratelimit

import (
	"time"
)

// MemoryBackend is the process-local exact 60-second sliding-window
// backend (the historical Limiter implementation). Distributed
// deployments should use PGWindowBackend instead; MemoryBackend
// remains the single-node default and the fail-open fallback.
type MemoryBackend struct {
	shards []shard
	now    func() time.Time
}

// NewMemoryBackend returns a MemoryBackend with default sharding.
func NewMemoryBackend() *MemoryBackend {
	b := &MemoryBackend{
		shards: make([]shard, defaultShardCount),
		now:    time.Now,
	}
	for i := range b.shards {
		b.shards[i].state = make(map[int64]*bucket)
	}
	return b
}

// SetNow overrides the wall clock (tests only).
func (b *MemoryBackend) SetNow(now func() time.Time) { b.now = now }

func (b *MemoryBackend) pickShard(key int64) *shard {
	// Knuth multiplicative hash spreads sequential IDs evenly.
	h := uint64(key) * 11400714819323198485
	return &b.shards[h%uint64(len(b.shards))]
}

func (b *MemoryBackend) AllowWindow(key int64, rpm, tpm int, promptTokens int, now time.Time) (bool, string) {
	if rpm == 0 && tpm == 0 {
		return true, ""
	}
	s := b.pickShard(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := now.Add(-60 * time.Second)
	bk, ok := s.state[key]
	if !ok {
		bk = &bucket{}
		s.state[key] = bk
	}
	i := 0
	for ; i < len(bk.requests); i++ {
		if bk.requests[i].After(cutoff) {
			break
		}
	}
	if i > 0 {
		bk.requests = bk.requests[i:]
		bk.tokens = bk.tokens[i:]
	}
	if rpm > 0 && len(bk.requests) >= rpm {
		return false, "rpm exceeded"
	}
	projected := 0
	for _, n := range bk.tokens {
		projected += n
	}
	if tpm > 0 && projected+promptTokens > tpm {
		return false, "tpm exceeded"
	}
	bk.requests = append(bk.requests, now)
	bk.tokens = append(bk.tokens, promptTokens)
	return true, ""
}

func (b *MemoryBackend) AccountWindow(key int64, extraTokens int, now time.Time) {
	if extraTokens <= 0 {
		return
	}
	s := b.pickShard(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	bk, ok := s.state[key]
	if !ok {
		return
	}
	if len(bk.tokens) > 0 {
		bk.tokens[len(bk.tokens)-1] += extraTokens
	}
}

func (b *MemoryBackend) AccountRequestWindow(key int64, now time.Time) {
	s := b.pickShard(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	bk, ok := s.state[key]
	if !ok {
		return
	}
	cutoff := now.Add(-60 * time.Second)
	i := 0
	for ; i < len(bk.requests); i++ {
		if bk.requests[i].After(cutoff) {
			break
		}
	}
	if i > 0 {
		bk.requests = bk.requests[i:]
		bk.tokens = bk.tokens[i:]
	}
	bk.requests = append(bk.requests, now)
	bk.tokens = append(bk.tokens, 0)
}

func (b *MemoryBackend) Reset() {
	for i := range b.shards {
		b.shards[i].mu.Lock()
		b.shards[i].state = make(map[int64]*bucket)
		b.shards[i].mu.Unlock()
	}
}

func (b *MemoryBackend) TrackedKeys() int {
	total := 0
	for i := range b.shards {
		b.shards[i].mu.Lock()
		total += len(b.shards[i].state)
		b.shards[i].mu.Unlock()
	}
	return total
}
