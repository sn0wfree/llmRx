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
		b.RecordFailure(1)
	}

	got := b.Filter(mkChannels(1))
	if len(got) != 0 {
		t.Fatalf("open: expected filter to drop channel, got %d", len(got))
	}
}

func TestBreaker_HalfOpenAfterReset(t *testing.T) {
	b := NewCircuitBreaker(nil)
	b.store = newStub(2, 30*time.Millisecond)

	b.RecordFailure(1)
	b.RecordFailure(1)
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

	b.RecordFailure(1)
	b.RecordFailure(1)
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

	b.RecordFailure(1)
	b.RecordFailure(1)
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
		b.RecordFailure(99)
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
	b.RecordFailure(99)
	b.RecordFailure(99)
	if got := b.Filter(mkChannels(99)); len(got) != 0 {
		t.Fatalf("expected open after 2 failures, got %d", len(got))
	}

	// Admin tightens the threshold to 1 — next failure should
	// re-trip immediately even though we already cleared state.
	live.maxFailures = 1
	b.reload(99)
	b.RecordFailure(99)
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
