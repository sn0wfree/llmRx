package router

import (
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestSplitByIntent(t *testing.T) {
	channels := []*model.Channel{
		{ID: 1, Name: "a", Intents: []string{"code", "chat"}},
		{ID: 2, Name: "b", Intents: []string{"chat"}},
		{ID: 3, Name: "c", Intents: nil},
	}
	n := splitByIntent(channels, "code")
	if n != 1 || channels[0].Name != "a" {
		t.Fatalf("matched: expected only 'a' at index 0, got n=%d head=%s", n, channels[0].Name)
	}
	if channels[1].Name != "b" || channels[2].Name != "c" {
		t.Fatalf("unmatched: expected b,c after partition, got %s,%s", channels[1].Name, channels[2].Name)
	}
}

func TestBreaker_SetDefaults(t *testing.T) {
	b := NewCircuitBreaker(nil)
	b.store = &stubStore{channels: map[int64]*model.Channel{
		1: {ID: 1, CircuitBreaker: model.CircuitBreakerConfig{MaxFailures: 0, ResetTimeout: 0}},
	}}
	live := &liveDefaults{maxFailures: 2, resetMs: 30}
	b.SetDefaults(live)
	maxFail, resetDur := b.cfgFor(1)
	if maxFail != 2 {
		t.Fatalf("expected maxFail=2 from live defaults, got %d", maxFail)
	}
	if resetDur != 30*1000*1000 {
		t.Fatalf("expected resetDur=30ms from live defaults, got %v", resetDur)
	}
}

func TestBreaker_SetDefaultsNil(t *testing.T) {
	b := NewCircuitBreaker(nil)
	b.store = newStub(0, 0)
	b.SetDefaults(nil)
	maxFail, resetDur := b.cfgFor(1)
	if maxFail != defaultMaxFailures {
		t.Fatalf("nil defaults: expected %d, got %d", defaultMaxFailures, maxFail)
	}
	if resetDur != defaultResetDur {
		t.Fatalf("nil defaults: expected %v, got %v", defaultResetDur, resetDur)
	}
}

func TestCostRouter_SetStrategyUnknown(t *testing.T) {
	r := &CostRouter{strategy: CheapestStrategy{}}
	r.SetStrategy("unknown_strategy")
	if r.Strategy() != model.StrategyCheapest {
		t.Fatalf("unknown strategy should fall back to cheapest, got %s", r.Strategy())
	}
}

func TestCostRouter_SetStrategyEmpty(t *testing.T) {
	r := &CostRouter{strategy: BalancedStrategy{}}
	r.SetStrategy("")
	if r.Strategy() != model.StrategyCheapest {
		t.Fatalf("empty strategy should fall back to cheapest, got %s", r.Strategy())
	}
}
