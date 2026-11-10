package router

import (
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// stubStore satisfies the small BreakerStore interface — consulted
// only when config is (re)pushed: startup priming and reload().
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

// prime pushes a per-channel breaker override into the breaker,
// mirroring what startup and the admin channel-update path do under
// the push-based contract.
func prime(b *CircuitBreaker, id int64, maxFail int, reset time.Duration) {
	b.PrimeChannel(&model.Channel{
		ID:             id,
		CircuitBreaker: model.CircuitBreakerConfig{MaxFailures: maxFail, ResetTimeout: reset},
	})
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
	prime(b, 1, 3, 50*time.Millisecond)
	ch := mkChannels(1)
	got := b.Filter(ch)
	if len(got) != 1 {
		t.Fatalf("closed: expected to pass, got %d", len(got))
	}
}

func TestBreaker_OpensAfterMaxFailures(t *testing.T) {
	b := NewCircuitBreaker(nil)
	prime(b, 1, 3, 50*time.Millisecond)

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
	prime(b, 1, 2, 30*time.Millisecond)

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
	prime(b, 1, 2, 30*time.Millisecond)

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
	b.PrimeAll([]model.Channel{
		{ID: 1, CircuitBreaker: model.CircuitBreakerConfig{MaxFailures: 2, ResetTimeout: 30 * time.Millisecond}},
		{ID: 2, CircuitBreaker: model.CircuitBreakerConfig{MaxFailures: 2, ResetTimeout: 30 * time.Millisecond}},
	})

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
	prime(b, 1, 2, 30*time.Millisecond)

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
	prime(b, 1, 50, 30*time.Millisecond)

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
	prime(b, 1, 50, 30*time.Millisecond)
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
	prime(b, 1, 50, 30*time.Millisecond)

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
	prime(b, 1, 50, 30*time.Millisecond)

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
	prime(b, 1, 3, 30*time.Millisecond)
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

// stubHealthChecker is a test HealthChecker that marks specific
// channel IDs as unhealthy.
type stubHealthChecker struct {
	unhealthy map[int64]bool
}

func (s *stubHealthChecker) Healthy(id int64) bool {
	return !s.unhealthy[id]
}

// TestBreaker_HealthCheckerFiltersUnhealthy verifies that a wired-in
// HealthChecker (prober) excludes channels it marks unhealthy,
// even when the breaker itself considers them healthy.
func TestBreaker_HealthCheckerFiltersUnhealthy(t *testing.T) {
	b := NewCircuitBreaker(nil)
	prime(b, 1, 50, 30*time.Millisecond)
	b.SetHealthChecker(&stubHealthChecker{unhealthy: map[int64]bool{2: true}})

	ch1 := &model.Channel{ID: 1, Name: "healthy-ch"}
	ch2 := &model.Channel{ID: 2, Name: "probe-unhealthy-ch"}
	got := b.Filter([]*model.Channel{ch1, ch2})
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("expected only healthy channel to pass, got %v", got)
	}
}

// TestBreaker_HealthCheckerSafetyNet verifies that when the
// HealthChecker marks every candidate unhealthy, the breaker
// falls back to the breaker-healthy list rather than returning
// an empty list (degraded attempt > no traffic).
func TestBreaker_HealthCheckerSafetyNet(t *testing.T) {
	b := NewCircuitBreaker(nil)
	prime(b, 1, 50, 30*time.Millisecond)
	b.SetHealthChecker(&stubHealthChecker{unhealthy: map[int64]bool{1: true, 2: true}})

	got := b.Filter(mkChannels(1, 2))
	if len(got) != 2 {
		t.Fatalf("safety net: expected all channels returned when prober excludes all, got %d", len(got))
	}
}

// TestBreaker_HealthCheckerNil is a regression test: nil
// HealthChecker must not affect filtering at all.
func TestBreaker_HealthCheckerNil(t *testing.T) {
	b := NewCircuitBreaker(nil)
	prime(b, 1, 50, 30*time.Millisecond)
	// No SetHealthChecker call: healthChecker is nil.
	got := b.Filter(mkChannels(1))
	if len(got) != 1 {
		t.Fatalf("nil health checker: expected channel to pass, got %d", len(got))
	}
}

// countingStore counts every GetChannel call so tests can pin the
// push-based contract: the request path never consults the store.
type countingStore struct {
	stubStore
	calls int
}

func (s *countingStore) GetChannel(id int64) (*model.Channel, error) {
	s.calls++
	return s.stubStore.GetChannel(id)
}

// TestBreaker_NoRequestPathStoreIO pins the push contract end to
// end: after PrimeChannel, routing decisions and failure recording
// must not consult the store; only reload() (admin-time) may pull
// exactly once, and the pulled override must take effect.
func TestBreaker_NoRequestPathStoreIO(t *testing.T) {
	st := &countingStore{stubStore: stubStore{channels: map[int64]*model.Channel{
		1: {ID: 1, CircuitBreaker: model.CircuitBreakerConfig{MaxFailures: 3, ResetTimeout: 30 * time.Millisecond}},
	}}}
	b := NewCircuitBreaker(st)
	b.PrimeChannel(&model.Channel{ID: 1, CircuitBreaker: model.CircuitBreakerConfig{MaxFailures: 3, ResetTimeout: 30 * time.Millisecond}})

	b.RecordFailure(1, 500)
	b.RecordSuccess(1)
	if got := b.Filter(mkChannels(1)); len(got) != 1 {
		t.Fatal("expected primed channel to pass")
	}
	if st.calls != 0 {
		t.Fatalf("request path consulted the store %d time(s)", st.calls)
	}

	// reload() is admin-time: it may pull the channel once to
	// re-prime, and the pulled override must take effect (open at
	// 3 failures, not at the 5-failure default).
	b.reload(1)
	if st.calls != 1 {
		t.Fatalf("reload should pull the channel exactly once, got %d", st.calls)
	}
	for i := 0; i < 3; i++ {
		b.RecordFailure(1, 500)
	}
	if got := b.Filter(mkChannels(1)); len(got) != 0 {
		t.Fatal("expected reload-pushed override (max 3) to open the breaker")
	}
	if st.calls != 1 {
		t.Fatalf("post-reload request path consulted the store: %d call(s)", st.calls)
	}
}

// TestBreaker_ReloadDropsOverrideForDeletedChannel: reload() on a
// channel that no longer exists clears any cached override, so the
// breaker does not keep applying config from a deleted row.
func TestBreaker_ReloadDropsOverrideForDeletedChannel(t *testing.T) {
	st := &countingStore{stubStore: stubStore{channels: map[int64]*model.Channel{}}}
	b := NewCircuitBreaker(st)
	prime(b, 1, 2, 30*time.Millisecond)
	b.reload(1) // channel gone from the store

	b.cfgMu.RLock()
	_, stillCached := b.cfgCache[1]
	b.cfgMu.RUnlock()
	if stillCached {
		t.Fatal("expected the deleted channel's override to be dropped")
	}
	// Falls back to defaults: 5 failures needed to open.
	for i := 0; i < 4; i++ {
		b.RecordFailure(1, 500)
	}
	if got := b.Filter(mkChannels(1)); len(got) != 1 {
		t.Fatal("expected default threshold (5) after override was dropped")
	}
	b.RecordFailure(1, 500)
	if got := b.Filter(mkChannels(1)); len(got) != 0 {
		t.Fatal("expected breaker to open at the default 5-failure threshold")
	}
}

// TestBreaker_DefaultsChangeKeepsOverrides pins the push semantics:
// refreshing the live defaults must NOT wipe per-channel overrides —
// before the PrimeChannel refactor refreshDefaults cleared the whole
// cfgCache, forcing every channel back through the store.
func TestBreaker_DefaultsChangeKeepsOverrides(t *testing.T) {
	b := NewCircuitBreaker(nil)
	prime(b, 1, 3, 30*time.Millisecond)
	live := &liveDefaults{maxFailures: 10, resetMs: 60}
	b.SetDefaults(live) // defaults change

	// Override (3) still wins over the new default (10).
	if mf, rd := b.cfgFor(1); mf != 3 || rd != 30*time.Millisecond {
		t.Fatalf("defaults change wiped the override: maxFail=%d reset=%v", mf, rd)
	}
	// A channel without an override does pick up the new default.
	if mf, _ := b.cfgFor(2); mf != 10 {
		t.Fatalf("new default not applied to unprimed channel: maxFail=%d", mf)
	}
}

// TestBreaker_OverrideZerosFallBackToDefaults: a primed entry whose
// row has no operator config (zeros) must resolve to the defaults,
// not to zero.
func TestBreaker_OverrideZerosFallBackToDefaults(t *testing.T) {
	b := NewCircuitBreaker(nil)
	b.PrimeChannel(&model.Channel{ID: 1}) // CircuitBreaker zero value
	mf, rd := b.cfgFor(1)
	if mf != defaultMaxFailures || rd != defaultResetDur {
		t.Fatalf("zero override must fall back to defaults, got maxFail=%d reset=%v", mf, rd)
	}
}

// TestBreaker_PrimeChannelNil: a nil channel is a no-op, not a panic.
func TestBreaker_PrimeChannelNil(t *testing.T) {
	b := NewCircuitBreaker(nil)
	b.PrimeChannel(nil)
	prime(b, 1, 4, time.Second) // cache still usable afterwards
	if mf, _ := b.cfgFor(1); mf != 4 {
		t.Fatalf("expected override after nil PrimeChannel, got %d", mf)
	}
}

// TestBreaker_PrimeAllReplacesCache: PrimeAll installs the given set
// wholesale — channels absent from the new set lose their override
// and fall back to defaults (a disabled/deleted row must not keep
// constraining the breaker).
func TestBreaker_PrimeAllReplacesCache(t *testing.T) {
	b := NewCircuitBreaker(nil)
	prime(b, 1, 3, 30*time.Millisecond)
	b.PrimeAll([]model.Channel{
		{ID: 2, CircuitBreaker: model.CircuitBreakerConfig{MaxFailures: 4, ResetTimeout: time.Second}},
	})
	if mf, _ := b.cfgFor(1); mf != defaultMaxFailures {
		t.Fatalf("stale override survived PrimeAll: maxFail=%d", mf)
	}
	if mf, rd := b.cfgFor(2); mf != 4 || rd != time.Second {
		t.Fatalf("new set not installed: maxFail=%d reset=%v", mf, rd)
	}
}

// TestBreaker_ResolveDefaultsClampsInvalid: a defaults source
// returning non-positive values must be clamped to the built-in
// constants, and a breaker built without NewCircuitBreaker (test
// construction) still resolves them.
func TestBreaker_ResolveDefaultsClampsInvalid(t *testing.T) {
	live := &liveDefaults{maxFailures: 0, resetMs: -5}
	b := &CircuitBreaker{defaults: live} // no NewCircuitBreaker: cold path
	mf, rd := b.cfgFor(1)
	if mf != defaultMaxFailures || rd != defaultResetDur {
		t.Fatalf("invalid defaults not clamped: maxFail=%d reset=%v", mf, rd)
	}
}

// TestBreaker_SetDefaultsClampsInvalid mirrors the clamp through the
// SetDefaults/refreshDefaults path (the one admin /runtime actually
// exercises).
func TestBreaker_SetDefaultsClampsInvalid(t *testing.T) {
	b := NewCircuitBreaker(nil)
	b.SetDefaults(&liveDefaults{maxFailures: -3, resetMs: 0})
	if mf, rd := b.cfgFor(1); mf != defaultMaxFailures || rd != defaultResetDur {
		t.Fatalf("invalid live defaults not clamped: maxFail=%d reset=%v", mf, rd)
	}
}
