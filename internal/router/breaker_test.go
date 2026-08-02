package router

import (
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// stubStore satisfies the small BreakerStore interface that the
// breaker depends on. Keeping it minimal makes tests straightforward.
type stubStore struct {
	channels map[int64]*model.Channel
}

func (s *stubStore) GetChannel(id int64) (*model.Channel, error) {
	if c, ok := s.channels[id]; ok {
		return c, nil
	}
	return nil, nil
}

func newStub(maxFail int, reset time.Duration) *stubStore {
	return &stubStore{
		channels: map[int64]*model.Channel{
			1: {
				ID: 1,
				CircuitBreaker: model.CircuitBreakerConfig{
					MaxFailures:  maxFail,
					ResetTimeout: reset,
				},
			},
		},
	}
}

func mkChannels(ids ...int64) []*model.Channel {
	out := make([]*model.Channel, 0, len(ids))
	for _, id := range ids {
		c := &model.Channel{ID: id, Name: "ch"}
		out = append(out, c)
	}
	return out
}

func TestBreaker_ClosedAllowsAll(t *testing.T) {
	b := NewCircuitBreaker(nil)
	b.store = newStub(3, 50*time.Millisecond)
	ch := mkChannels(1)
	got := b.Filter(ch)
	if len(got) != 1 {
		t.Fatalf("closed: expected to pass, got %d", len(got))
	}
}

func TestBreaker_OpensAfterMaxFailures(t *testing.T) {
	b := NewCircuitBreaker(nil)
	b.store = newStub(3, 50*time.Millisecond)

	for i := 0; i < 3; i++ {
		b.RecordFailure(1, 500)
	}

	got := b.Filter(mkChannels(1))
	if len(got) != 0 {
		t.Fatalf("open: expected filter to drop channel, got %d", len(got))
	}
}

func TestBreaker_HalfOpenAfterReset(t *testing.T) {
	b := NewCircuitBreaker(nil)
	b.store = newStub(2, 30*time.Millisecond)

	b.RecordFailure(1, 500)
	b.RecordFailure(1, 500)
	if got := b.Filter(mkChannels(1)); len(got) != 0 {
		t.Fatalf("expected open after 2 failures, got %d", len(got))
	}

	time.Sleep(40 * time.Millisecond)
	got := b.Filter(mkChannels(1))
	if len(got) != 1 {
		t.Fatalf("half-open: expected channel to be admitted after reset window, got %d", len(got))
	}
}

func TestBreaker_RecordSuccessResetsState(t *testing.T) {
	b := NewCircuitBreaker(nil)
	b.store = newStub(2, 30*time.Millisecond)

	b.RecordFailure(1, 500)
	b.RecordFailure(1, 500)
	if got := b.Filter(mkChannels(1)); len(got) != 0 {
		t.Fatal("expected open before success")
	}

	b.RecordSuccess(1)
	if got := b.Filter(mkChannels(1)); len(got) != 1 {
		t.Fatal("expected success to close breaker immediately")
	}
}

func TestBreaker_PartialOpenFiltersOnlyAffected(t *testing.T) {
	b := NewCircuitBreaker(nil)
	b.store = &stubStore{channels: map[int64]*model.Channel{
		1: {ID: 1, CircuitBreaker: model.CircuitBreakerConfig{MaxFailures: 2, ResetTimeout: 30 * time.Millisecond}},
		2: {ID: 2, CircuitBreaker: model.CircuitBreakerConfig{MaxFailures: 2, ResetTimeout: 30 * time.Millisecond}},
	}}

	b.RecordFailure(1, 500)
	b.RecordFailure(1, 500)
	// 2 is healthy, 1 should be filtered.

	got := b.Filter(mkChannels(1, 2))
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("partial open: expected only ch2 to pass, got %v", got)
	}
}

func TestBreaker_DefaultsWhenChannelMissing(t *testing.T) {
	// channel id 99 not in store → defaults (5/60s) apply
	b := &CircuitBreaker{
		store: &stubStore{channels: map[int64]*model.Channel{}},
	}

	for i := 0; i < 5; i++ {
		b.RecordFailure(99, 500)
	}
	if got := b.Filter(mkChannels(99)); len(got) != 0 {
		t.Fatalf("defaults: expected open after 5 failures, got %d", len(got))
	}
}

// TestBreaker_LiveDefaultsTakeEffect verifies that admin /runtime
// config writes to BreakerMaxFailures / BreakerResetTimeoutMs
// flow into the breaker on the next failure, without needing a
// restart. Before the SetDefaults wiring, cfgFor only consulted
// channel.CircuitBreaker and the runtime snapshot was ignored.
func TestBreaker_LiveDefaultsTakeEffect(t *testing.T) {
	live := &liveDefaults{maxFailures: 2, resetMs: 30}
	b := &CircuitBreaker{
		store:    &stubStore{channels: map[int64]*model.Channel{}},
		defaults: live,
	}

	// Trip the breaker at the live-defaults threshold (2).
	b.RecordFailure(99, 500)
	b.RecordFailure(99, 500)
	if got := b.Filter(mkChannels(99)); len(got) != 0 {
		t.Fatalf("expected open after 2 failures, got %d", len(got))
	}

	// Admin tightens the threshold to 1 — next failure should
	// re-trip immediately even though we already cleared state.
	live.maxFailures = 1
	b.reload(99)
	b.RecordFailure(99, 500)
	if got := b.Filter(mkChannels(99)); len(got) != 0 {
		t.Fatalf("tightened default to 1: expected open, got %d", len(got))
	}

	// Admin extends the reset window — the breaker must NOT
	// half-open before the new window elapses.
	live.resetMs = int64((24 * time.Hour) / time.Millisecond)
	if got := b.Filter(mkChannels(99)); len(got) != 0 {
		t.Fatalf("extended reset window: expected still open, got %d", len(got))
	}
}

// liveDefaults is a tiny BreakerDefaults implementation that
// returns mutable values so tests can simulate admin writes
// between calls.
type liveDefaults struct {
	maxFailures int64
	resetMs     int64
}

func (l *liveDefaults) BreakerMaxFailures() int64    { return l.maxFailures }
func (l *liveDefaults) BreakerResetTimeoutMs() int64 { return l.resetMs }

// compile-time check
var _ BreakerDefaults = (*liveDefaults)(nil)

// TestBreaker_429Cooldown: a 429 parks the channel for a short
// cooldown but never counts toward the consecutive-failure counter.
func TestBreaker_429Cooldown(t *testing.T) {
	b := NewCircuitBreaker(nil)
	b.store = newStub(2, 30*time.Millisecond)

	for i := 0; i < 5; i++ {
		b.RecordFailure(1, 429)
	}
	entry := b.getEntry(1)
	entry.mu.Lock()
	failures := entry.failures
	isOpen := entry.isOpen
	entry.mu.Unlock()
	if failures != 0 || isOpen {
		t.Fatalf("429s must not count toward failures: failures=%d isOpen=%v", failures, isOpen)
	}
	// Immediately after the 429s the channel is parked.
	if got := b.Filter(mkChannels(1)); len(got) != 0 {
		t.Fatal("expected 429 cooldown to filter the channel")
	}
	// A success clears the cooldown.
	b.RecordSuccess(1)
	if got := b.Filter(mkChannels(1)); len(got) != 1 {
		t.Fatal("expected success to clear the 429 cooldown")
	}
}

// TestBreaker_401HardReject: auth/config errors park the channel
// until an operator reload or a success — retrying is pointless.
func TestBreaker_401HardReject(t *testing.T) {
	b := NewCircuitBreaker(nil)
	b.store = newStub(50, 30*time.Millisecond)

	b.RecordFailure(1, 401)
	if got := b.Filter(mkChannels(1)); len(got) != 0 {
		t.Fatal("expected hard reject to filter the channel")
	}
	// Breaker opening never happens for hard rejects.
	entry := b.getEntry(1)
	entry.mu.Lock()
	failures := entry.failures
	isOpen := entry.isOpen
	entry.mu.Unlock()
	if failures != 0 || isOpen {
		t.Fatalf("hard reject must not count toward failures: failures=%d isOpen=%v", failures, isOpen)
	}
	// Operator reload clears the reject.
	b.reload(1)
	if got := b.Filter(mkChannels(1)); len(got) != 1 {
		t.Fatal("expected reload to clear the hard reject")
	}
}

// TestBreaker_404HardReject mirrors 401 for not-found upstreams.
func TestBreaker_404HardReject(t *testing.T) {
	b := NewCircuitBreaker(nil)
	b.store = newStub(50, 30*time.Millisecond)
	b.RecordFailure(1, 404)
	if got := b.Filter(mkChannels(1)); len(got) != 0 {
		t.Fatal("expected 404 hard reject to filter the channel")
	}
	b.RecordSuccess(1)
	if got := b.Filter(mkChannels(1)); len(got) != 1 {
		t.Fatal("expected success to clear the hard reject")
	}
}

// TestBreaker_MinuteRateCooldown: a sustained >50% failure rate
// over the last minute excludes the channel at selection time even
// when the consecutive counter is far below the open threshold.
// The exclusion is pure sliding-window: enough success samples
// push the rate back below the threshold.
func TestBreaker_MinuteRateCooldown(t *testing.T) {
	b := NewCircuitBreaker(nil)
	b.store = newStub(50, 30*time.Millisecond)

	for i := 0; i < 7; i++ {
		b.RecordFailure(1, 500)
	}
	for i := 0; i < 3; i++ {
		b.RecordSuccess(1)
	}
	// 7 fails / 10 attempts = 70% > 50% -> excluded by the window.
	if got := b.Filter(mkChannels(1)); len(got) != 0 {
		t.Fatal("expected minute-rate exclusion to filter the channel")
	}
	// Push the rate below 50% with successes (7/20 = 35%).
	for i := 0; i < 10; i++ {
		b.RecordSuccess(1)
	}
	if got := b.Filter(mkChannels(1)); len(got) != 1 {
		t.Fatal("expected success samples to clear the minute-rate exclusion")
	}
}

// TestBreaker_HealthyRateNoCooldown: 10 observations at or below
// the 50% threshold must not park the channel.
func TestBreaker_HealthyRateNoCooldown(t *testing.T) {
	b := NewCircuitBreaker(nil)
	b.store = newStub(50, 30*time.Millisecond)

	for i := 0; i < 5; i++ {
		b.RecordFailure(1, 500)
		b.RecordSuccess(1)
	}
	if got := b.Filter(mkChannels(1)); len(got) != 1 {
		t.Fatal("50%% failure rate must not trip the cooldown")
	}
}

// TestBreaker_5xxOpensBreaker: non-429/401/404 failures keep the
// existing consecutive-failure semantics.
func TestBreaker_5xxOpensBreaker(t *testing.T) {
	b := NewCircuitBreaker(nil)
	b.store = newStub(3, 30*time.Millisecond)
	for i := 0; i < 3; i++ {
		b.RecordFailure(1, 503)
	}
	if got := b.Filter(mkChannels(1)); len(got) != 0 {
		t.Fatal("expected breaker to open after 3x 503")
	}
	entry := b.getEntry(1)
	entry.mu.Lock()
	isOpen := entry.isOpen
	entry.mu.Unlock()
	if !isOpen {
		t.Fatal("expected isOpen after 3x 503")
	}
}
