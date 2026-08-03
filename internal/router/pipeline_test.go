package router

import (
	"sync"
	"testing"
)

// TestRctxPool_RaceFree verifies that the RouteContext pool is safe
// for concurrent access. This exercises the sync.Pool path directly
// rather than going through a full engine (which needs a store).
func TestRctxPool_RaceFree(t *testing.T) {
	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			rctx := rctxPool.Get().(*RouteContext)
			rctx.Candidates = nil
			rctx.LogParts = nil
			rctxPool.Put(rctx)
		}()
	}
	wg.Wait()
}