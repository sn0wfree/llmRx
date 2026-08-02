package auto

import "testing"

func TestStats_RecordAndSnapshot(t *testing.T) {
	s := NewStats()
	empty := s.Snapshot()
	if empty.Decisions != 0 || len(empty.TierHits) != 0 {
		t.Fatalf("fresh stats: %+v", empty)
	}

	s.Record(Decision{Tier: "simple", Cause: "heuristic", Routed: "m1"})
	s.Record(Decision{Tier: "complex", Cause: "llm", Routed: "m2"})
	s.Record(Decision{Tier: "simple", Cause: "heuristic_fallback", Routed: "m3", Fallback: true})

	snap := s.Snapshot()
	if snap.Decisions != 3 {
		t.Fatalf("decisions = %d, want 3", snap.Decisions)
	}
	if snap.Routed != 3 {
		t.Fatalf("routed = %d, want 3", snap.Routed)
	}
	if snap.Fallbacks != 1 {
		t.Fatalf("fallbacks = %d, want 1", snap.Fallbacks)
	}
	if snap.TierHits["simple"] != 2 || snap.TierHits["complex"] != 1 {
		t.Fatalf("tier hits: %+v", snap.TierHits)
	}
	if snap.CauseHits["heuristic"] != 1 || snap.CauseHits["llm"] != 1 || snap.CauseHits["heuristic_fallback"] != 1 {
		t.Fatalf("cause hits: %+v", snap.CauseHits)
	}

	s.Reset()
	after := s.Snapshot()
	if after.Decisions != 0 || len(after.TierHits) != 0 || len(after.CauseHits) != 0 {
		t.Fatalf("after reset: %+v", after)
	}
}

func TestStats_NoRoutedDecision(t *testing.T) {
	s := NewStats()
	s.Record(Decision{Tier: "simple", Cause: "heuristic", Routed: ""})
	snap := s.Snapshot()
	if snap.Routed != 0 {
		t.Fatalf("routed = %d, want 0", snap.Routed)
	}
}

// TestStats_Concurrent: records from parallel goroutines must not
// lose updates (race detector).
func TestStats_Concurrent(t *testing.T) {
	s := NewStats()
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 100; i++ {
				s.Record(Decision{Tier: "simple", Cause: "heuristic", Routed: "m"})
				_ = s.Snapshot()
			}
		}()
	}
	for g := 0; g < 8; g++ {
		<-done
	}
	if snap := s.Snapshot(); snap.Decisions != 800 {
		t.Fatalf("decisions = %d, want 800 (lost updates)", snap.Decisions)
	}
}
