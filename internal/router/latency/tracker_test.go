package latency

import (
	"sync"
	"testing"
)

func TestTracker_FirstObservationSetsEWMA(t *testing.T) {
	tr := New(0.1)
	tr.Record(1, 100)
	if got := tr.LatencyMS(1); got != 100 {
		t.Fatalf("first observation: want 100, got %f", got)
	}
}

func TestTracker_EWMAUpdates(t *testing.T) {
	tr := New(0.5)
	tr.Record(1, 100) // ewma = 100
	tr.Record(1, 200) // ewma = 0.5*200 + 0.5*100 = 150
	if got := tr.LatencyMS(1); got != 150 {
		t.Fatalf("after 2 obs: want 150, got %f", got)
	}
	tr.Record(1, 200) // ewma = 0.5*200 + 0.5*150 = 175
	if got := tr.LatencyMS(1); got != 175 {
		t.Fatalf("after 3 obs: want 175, got %f", got)
	}
}

func TestTracker_UnknownReturnsZero(t *testing.T) {
	tr := New(0.1)
	if got := tr.LatencyMS(999); got != 0 {
		t.Fatalf("unknown channel: want 0, got %f", got)
	}
}

func TestTracker_NegativeDurationClamped(t *testing.T) {
	tr := New(0.1)
	tr.Record(1, -50)
	if got := tr.LatencyMS(1); got != 0 {
		t.Fatalf("negative: want 0 (clamped), got %f", got)
	}
}

func TestTracker_Snapshot(t *testing.T) {
	tr := New(0.1)
	tr.Record(1, 100)
	tr.Record(2, 200)
	snap := tr.Snapshot()
	if snap[1] != 100 || snap[2] != 200 || len(snap) != 2 {
		t.Fatalf("snapshot mismatch: %+v", snap)
	}
	// Mutate snapshot: internal state must be unaffected.
	snap[1] = 999
	if tr.LatencyMS(1) != 100 {
		t.Fatalf("snapshot is not a copy")
	}
}

func TestTracker_Reset(t *testing.T) {
	tr := New(0.1)
	tr.Record(1, 100)
	tr.Record(2, 200)
	tr.Reset()
	if got := tr.LatencyMS(1); got != 0 {
		t.Fatalf("after reset: want 0, got %f", got)
	}
	if snap := tr.Snapshot(); len(snap) != 0 {
		t.Fatalf("after reset: want empty snapshot, got %+v", snap)
	}
}

func TestTracker_DefaultAlpha(t *testing.T) {
	tr := New(0) // 0 -> DefaultAlpha
	if tr.alpha != DefaultAlpha {
		t.Fatalf("want default alpha %f, got %f", DefaultAlpha, tr.alpha)
	}
	tr = New(-1)
	if tr.alpha != DefaultAlpha {
		t.Fatalf("want default alpha for negative input, got %f", tr.alpha)
	}
	tr = New(2) // > 1 -> DefaultAlpha
	if tr.alpha != DefaultAlpha {
		t.Fatalf("want default alpha for >1 input, got %f", tr.alpha)
	}
}

func TestTracker_Concurrent(t *testing.T) {
	tr := New(0.1)
	var wg sync.WaitGroup
	const goroutines = 50
	const iterations = 100
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			ch := int64(g % 10)
			for i := 0; i < iterations; i++ {
				tr.Record(ch, float64(100+i))
				_ = tr.LatencyMS(ch)
				_ = tr.Snapshot()
			}
		}(g)
	}
	wg.Wait()
	// Just verify we didn't panic; values are non-deterministic.
	if snap := tr.Snapshot(); len(snap) == 0 {
		t.Fatal("expected some data after concurrent writes")
	}
}