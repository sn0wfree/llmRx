// Package latency tracks per-channel exponentially weighted moving
// average (EWMA) response times. The tracker is consulted by the
// latency-aware routing strategies to prefer channels that respond
// faster, without the memory overhead of a percentile ring buffer.
//
// EWMA is O(1) per update and O(1) memory per channel. It is less
// sensitive to tail latency than a p95/p99 ring buffer, but
// sufficient for routing decisions where a rough "is this channel
// slow?" signal is enough.
package latency

import (
	"sync"
)

// DefaultAlpha is the EWMA smoothing factor. A value of 0.1 means
// each new sample contributes 10% to the moving average - the
// average reacts to trend changes within ~10 samples while
// absorbing single-sample spikes.
const DefaultAlpha = 0.1

// Tracker maintains per-channel EWMA latency in milliseconds.
// It is safe for concurrent use: Record and LatencyMS take a
// read-lock or write-lock on a single mutex, and contention is
// negligible (one Record per request, one LatencyMS per route).
type Tracker struct {
	mu    sync.RWMutex
	ewmas map[int64]float64 // channelID -> EWMA ms
	alpha float64
}

// New returns a Tracker with the given EWMA alpha (0 < alpha <= 1).
// Pass 0 to use DefaultAlpha.
func New(alpha float64) *Tracker {
	if alpha <= 0 || alpha > 1 {
		alpha = DefaultAlpha
	}
	return &Tracker{
		ewmas: make(map[int64]float64),
		alpha: alpha,
	}
}

// Record updates the EWMA for channelID with the observed duration
// in milliseconds. The first observation sets the EWMA directly
// (no prior average to smooth). Subsequent observations use
// ewma = alpha*new + (1-alpha)*old.
func (t *Tracker) Record(channelID int64, durationMs float64) {
	if durationMs < 0 {
		durationMs = 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	old, ok := t.ewmas[channelID]
	if !ok || old == 0 {
		t.ewmas[channelID] = durationMs
		return
	}
	t.ewmas[channelID] = t.alpha*durationMs + (1-t.alpha)*old
}

// LatencyMS returns the current EWMA for channelID. Returns 0
// when the channel has no observations (cold start); callers
// should treat 0 as "unknown" and not penalise the channel.
func (t *Tracker) LatencyMS(channelID int64) float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.ewmas[channelID]
}

// Snapshot returns a copy of all per-channel EWMA values. Used by
// the admin state endpoint and tests.
func (t *Tracker) Snapshot() map[int64]float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[int64]float64, len(t.ewmas))
	for k, v := range t.ewmas {
		out[k] = v
	}
	return out
}

// Reset clears all EWMA data. Called from admin /reload.
func (t *Tracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ewmas = make(map[int64]float64)
}

// SetAlpha overrides the EWMA smoothing factor. Values outside
// (0, 1] are ignored. Safe for concurrent use (the factor is read
// under the same mutex as Record).
func (t *Tracker) SetAlpha(alpha float64) {
	if alpha <= 0 || alpha > 1 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.alpha = alpha
}

// Alpha returns the current smoothing factor.
func (t *Tracker) Alpha() float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.alpha
}
