package auto

import "sync"

// Stats accumulates per-request decision counters for the admin
// state endpoint ("看见它在进化"). All methods are safe for
// concurrent use; contention is negligible (one record per auto
// request).
type Stats struct {
	mu        sync.Mutex
	decisions int64
	routed    int64
	fallbacks int64
	tierHits  map[string]int64
	causeHits map[string]int64
}

// NewStats returns an empty accumulator.
func NewStats() *Stats {
	return &Stats{
		tierHits:  make(map[string]int64),
		causeHits: make(map[string]int64),
	}
}

// Record folds one decision into the counters.
func (s *Stats) Record(d Decision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.decisions++
	if d.Routed != "" {
		s.routed++
	}
	if d.Fallback {
		s.fallbacks++
	}
	s.tierHits[d.Tier]++
	s.causeHits[d.Cause]++
}

// StatsSnapshot is a point-in-time view for the admin endpoint.
type StatsSnapshot struct {
	Decisions int64            `json:"decisions"`
	Routed    int64            `json:"routed"`
	Fallbacks int64            `json:"fallbacks"`
	TierHits  map[string]int64 `json:"tier_hits"`
	CauseHits map[string]int64 `json:"cause_hits"`
}

// Snapshot returns a consistent copy of the counters.
func (s *Stats) Snapshot() StatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := StatsSnapshot{
		Decisions: s.decisions,
		Routed:    s.routed,
		Fallbacks: s.fallbacks,
		TierHits:  make(map[string]int64, len(s.tierHits)),
		CauseHits: make(map[string]int64, len(s.causeHits)),
	}
	for k, v := range s.tierHits {
		out.TierHits[k] = v
	}
	for k, v := range s.causeHits {
		out.CauseHits[k] = v
	}
	return out
}

// Reset zeroes every counter. Called from admin /reload together
// with the Thompson arm reset.
func (s *Stats) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.decisions = 0
	s.routed = 0
	s.fallbacks = 0
	s.tierHits = make(map[string]int64)
	s.causeHits = make(map[string]int64)
}
