package intent

import (
	"sync"
	"sync/atomic"
	"testing"
)

// BenchmarkNativeClassify_Parallel exercises the native Classify
// path under concurrent goroutines. If the mutex is removed, this
// should saturate the cgo throughput. If the mutex is in place,
// throughput is capped at 1/(cgo latency).
func BenchmarkNativeClassify_Parallel(b *testing.B) {
	c, err := Load()
	if err != nil {
		b.Skipf("intent .so not available: %v", err)
	}
	defer c.Close()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = c.Classify("def hello(): return 42")
		}
	})
}

// TestNativeClassify_ConcurrentNoCorruption hammers Classify
// concurrently to detect buffer corruption / race in the cgo
// path. Safe to run under -race (the Rust side is stateless).
func TestNativeClassify_ConcurrentNoCorruption(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Skipf("intent .so not available: %v", err)
	}
	defer c.Close()

	const goroutines = 32
	const perG = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	var fails int64
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				r := c.Classify("the quick brown fox jumps over the lazy dog")
				// Every call should return a non-empty kind label
				// from the keyword scorer. Anything else (empty
				// label, garbage) indicates buffer corruption.
				if r.Kind == "" {
					atomic.AddInt64(&fails, 1)
				}
			}
		}()
	}
	wg.Wait()
	if fails > 0 {
		t.Fatalf("%d classification(s) returned an empty kind", fails)
	}
}
