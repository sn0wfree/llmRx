package ratelimit

import (
	"testing"
)

// BenchmarkLimiter_Allow_AtomicNow measures the Allow path on the
// post-atomic.Value version: a single atomic.Load to read the
// now function instead of an RLock/RUnlock pair on every call.
//
// rpm/tpm are set huge so the bucket ceiling is never hit during
// the benchmark; the goal is to exercise the atomic.Load on nowFn()
// inside the sharded lock path.
func BenchmarkLimiter_Allow_AtomicNow(b *testing.B) {
	l := New()
	const key int64 = 42
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ok, _ := l.Allow(key, 1<<30, 1<<30, 0, 0, 0, 0)
		if !ok {
			b.Fatal("rate limit denied")
		}
	}
}

// BenchmarkLimiter_AllowMultiKey_AtomicNow same as above but
// across 64 distinct keys so the shard contention shows up.
func BenchmarkLimiter_AllowMultiKey_AtomicNow(b *testing.B) {
	l := New()
	const keys = 64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ok, _ := l.Allow(int64(i%keys), 1<<30, 1<<30, 0, 0, 0, 0)
		if !ok {
			b.Fatal("rate limit denied")
		}
	}
}
