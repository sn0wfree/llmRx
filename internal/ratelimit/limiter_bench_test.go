package ratelimit

import (
	"sync"
	"sync/atomic"
	"testing"
)

// BenchmarkLimiter_Allow measures the cost of a single Allow check
// — this runs on every LLM request. Target: sub-microsecond.
func BenchmarkLimiter_Allow(b *testing.B) {
	l := New()
	var hits int64

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, _ = l.Allow(1, 600, 100000, 100); true {
				atomic.AddInt64(&hits, 1)
			}
		}
	})
	_ = hits
}

// BenchmarkLimiter_AllowMultiKey measures per-key throughput with
// many concurrent tokens. The map contention is the real cost.
func BenchmarkLimiter_AllowMultiKey(b *testing.B) {
	l := New()
	for i := 0; i < 100; i++ {
		_, _ = l.Allow(int64(i), 600, 100000, 100)
	}

	var wg sync.WaitGroup
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_, _ = l.Allow(int64(id%100), 600, 100000, 100)
		}(i)
	}
	wg.Wait()
}
