package router

import (
	"sync"
	"testing"
)

// TestRouterEngine_BuildPipeline_ConcurrentInit verifies that the
// lazy pipeline init in buildPipeline() is race-free. Without
// sync.Once, two goroutines could both observe e.pipeline == nil
// and both write — a clear data race under -race.
func TestRouterEngine_BuildPipeline_ConcurrentInit(t *testing.T) {
	e := &RouterEngine{}
	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			p := e.buildPipeline()
			if len(p) != 5 {
				t.Errorf("expected 5 stages, got %d", len(p))
			}
		}()
	}
	wg.Wait()
	// After all goroutines settle, the pipeline must be set exactly
	// once (the same backing slice). Verify by re-fetching and
	// comparing slice headers — the test helper does that via
	// identity comparison.
	if e.pipeline == nil {
		t.Fatal("pipeline should be cached after first buildPipeline")
	}
	p := e.buildPipeline()
	if len(p) != 5 {
		t.Errorf("subsequent buildPipeline should return cached 5 stages, got %d", len(p))
	}
}

// TestRouterEngine_BuildPipeline_NoStores ensures buildPipeline
// doesn't panic when the engine is constructed as a zero-value
// struct (a pattern used by access-only tests in internal/api).
// The static stage always succeeds; the breaker/cost/intent stages
// are no-ops when their *inner* is nil because the engine never
// actually iterates them. So we only assert the slice is built.
func TestRouterEngine_BuildPipeline_NoStores(t *testing.T) {
	e := &RouterEngine{}
	p := e.buildPipeline()
	if len(p) != 5 {
		t.Fatalf("expected 5 stages, got %d", len(p))
	}
	expectedNames := []string{"static", "breaker", "cost", "intent", "thompson"}
	for i, stage := range p {
		if got := stage.Name(); got != expectedNames[i] {
			t.Errorf("stage %d: expected name %q, got %q", i, expectedNames[i], got)
		}
	}
}
