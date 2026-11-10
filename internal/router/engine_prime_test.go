package router

import (
	"errors"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/store"
)

// primeStub serves a fixed channel list to the engine. Only
// GetChannels/GetChannel are consulted on the paths under test, so a
// nil-embedded Store suffices; any other call would panic in tests.
type primeStub struct {
	store.Store
	chans []model.Channel
}

func (p *primeStub) GetChannels() ([]model.Channel, error) { return p.chans, nil }

// failingStub reports a store error for GetChannels.
type failingStub struct {
	store.Store
}

func (f *failingStub) GetChannels() ([]model.Channel, error) {
	return nil, errors.New("db down")
}

// stubLocalQuota is a UsageProvider returning a fixed local ratio.
type stubLocalQuota struct{ ratio float64 }

func (s *stubLocalQuota) UsageRatio(*model.Channel, time.Time) float64 { return s.ratio }

// stubVendorQuota is a VendorUsageProvider whose reading may be live
// or stale, mirroring subusage.SourceRatio's contract.
type stubVendorQuota struct {
	ratio float64
	ok    bool
}

func (s *stubVendorQuota) UsageRatio(*model.Channel, time.Time) float64 { return s.ratio }
func (s *stubVendorQuota) SourceRatio(*model.Channel, time.Time) (float64, bool) {
	return s.ratio, s.ok
}

// TestEngine_QuotaSourceCombining pins rebuildQuotaSource across all
// four wiring combinations: nothing wired disables the guard, vendor
// wins over local when live, local is the fallback when the vendor
// reading is stale, and each source alone stands on its own.
func TestEngine_QuotaSourceCombining(t *testing.T) {
	st := &primeStub{chans: []model.Channel{{ID: 1}}}
	eng := New(st, pool.NewChannelPool())
	ch := &model.Channel{ID: 1}
	now := time.Now()

	// Nothing wired: no quota provider at all.
	eng.SetUsageTracker(nil)
	eng.SetVendorUsageTracker(nil)
	if eng.usage != nil {
		t.Fatal("expected no quota provider when nothing is wired")
	}

	// Local only.
	local := &stubLocalQuota{ratio: 0.4}
	eng.SetUsageTracker(local)
	eng.SetVendorUsageTracker(nil)
	if r := eng.usage.UsageRatio(ch, now); r != 0.4 {
		t.Fatalf("local-only ratio = %v, want 0.4", r)
	}
	q, isQ := eng.usage.(*quotaProvider)
	if !isQ {
		t.Fatalf("expected a *quotaProvider, got %T", eng.usage)
	}
	if _, has := q.SourceRatio(ch, now); has {
		t.Fatal("local-only wiring must not report vendor data")
	}

	// Vendor only.
	eng.SetUsageTracker(nil)
	eng.SetVendorUsageTracker(&stubVendorQuota{ratio: 0.9, ok: true})
	if r := eng.usage.UsageRatio(ch, now); r != 0.9 {
		t.Fatalf("vendor-only ratio = %v, want 0.9", r)
	}

	// Both, vendor live: vendor wins.
	eng.SetUsageTracker(local)
	eng.SetVendorUsageTracker(&stubVendorQuota{ratio: 0.9, ok: true})
	if r := eng.usage.UsageRatio(ch, now); r != 0.9 {
		t.Fatalf("live vendor must win: ratio = %v", r)
	}

	// Both, vendor stale: local fallback.
	eng.SetVendorUsageTracker(&stubVendorQuota{ratio: 0.9, ok: false})
	if r := eng.usage.UsageRatio(ch, now); r != 0.4 {
		t.Fatalf("stale vendor must fall back to local: ratio = %v", r)
	}
}

// TestEngine_NewPrimesBreakerOverrides pins the startup push: the
// engine constructor must install every channel's circuit-breaker
// override from the store, so the first request already resolves
// config from cache — no cold-start GetChannel on the request path.
func TestEngine_NewPrimesBreakerOverrides(t *testing.T) {
	st := &primeStub{chans: []model.Channel{
		{ID: 1, CircuitBreaker: model.CircuitBreakerConfig{MaxFailures: 3}},
	}}
	eng := New(st, pool.NewChannelPool())
	if mf, _ := eng.breaker.cfgFor(1); mf != 3 {
		t.Fatalf("startup priming missed the override: maxFail=%d (want 3)", mf)
	}
	// Unknown channels run on defaults.
	if mf, _ := eng.breaker.cfgFor(99); mf != defaultMaxFailures {
		t.Fatalf("unprimed channel: maxFail=%d", mf)
	}
}

// TestEngine_ReloadAllRePrimesBreaker pins the /reload push: breaker
// overrides dropped and re-installed from the current DB state, so a
// row edited outside the admin path takes effect after ReloadAll.
func TestEngine_ReloadAllRePrimesBreaker(t *testing.T) {
	st := &primeStub{chans: []model.Channel{
		{ID: 1, CircuitBreaker: model.CircuitBreakerConfig{MaxFailures: 3}},
	}}
	eng := New(st, pool.NewChannelPool())

	// Operator edits the row directly in the DB; the breaker cache is
	// now stale (still 3).
	st.chans[0].CircuitBreaker.MaxFailures = 2
	eng.ReloadAllChannels()
	if mf, _ := eng.breaker.cfgFor(1); mf != 2 {
		t.Fatalf("ReloadAllChannels did not re-prime from the DB: maxFail=%d (want 2)", mf)
	}
}

// TestEngine_NewStoreErrorFallsBackToDefaults: if the store is down
// at construction the engine must still come up, running the breaker
// on built-in defaults.
func TestEngine_NewStoreErrorFallsBackToDefaults(t *testing.T) {
	eng := New(&failingStub{}, pool.NewChannelPool())
	if mf, _ := eng.breaker.cfgFor(1); mf != defaultMaxFailures {
		t.Fatalf("store error at startup must leave defaults, got maxFail=%d", mf)
	}
}
