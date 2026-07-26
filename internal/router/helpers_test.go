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
	matched, unmatched := splitByIntent(channels, "code")
	if len(matched) != 1 || matched[0].Name != "a" {
		t.Fatalf("matched: expected only 'a', got %v", matched)
	}
	if len(unmatched) != 2 {
		t.Fatalf("unmatched: expected 2, got %d", len(unmatched))
	}
}

func TestSplitByIntent_NoMatches(t *testing.T) {
	channels := []*model.Channel{
		{ID: 1, Name: "a", Intents: []string{"chat"}},
		{ID: 2, Name: "b"},
	}
	matched, unmatched := splitByIntent(channels, "code")
	if len(matched) != 0 {
		t.Fatalf("expected 0 matched, got %d", len(matched))
	}
	if len(unmatched) != 2 {
		t.Fatalf("expected 2 unmatched, got %d", len(unmatched))
	}
}

func TestSplitByIntent_AllMatch(t *testing.T) {
	channels := []*model.Channel{
		{ID: 1, Name: "a", Intents: []string{"code"}},
		{ID: 2, Name: "b", Intents: []string{"code", "chat"}},
	}
	matched, unmatched := splitByIntent(channels, "code")
	if len(matched) != 2 {
		t.Fatalf("expected 2 matched, got %d", len(matched))
	}
	if len(unmatched) != 0 {
		t.Fatalf("expected 0 unmatched, got %d", len(unmatched))
	}
}

func TestSplitByIntent_Empty(t *testing.T) {
	matched, unmatched := splitByIntent(nil, "code")
	if len(matched) != 0 || len(unmatched) != 0 {
		t.Fatalf("expected empty results for nil input")
	}
}

func TestContainsString(t *testing.T) {
	if !containsString([]string{"a", "b", "c"}, "b") {
		t.Fatal("expected true for 'b' in [a,b,c]")
	}
	if containsString([]string{"a", "b", "c"}, "d") {
		t.Fatal("expected false for 'd' in [a,b,c]")
	}
	if containsString(nil, "a") {
		t.Fatal("expected false for nil slice")
	}
	if containsString([]string{}, "a") {
		t.Fatal("expected false for empty slice")
	}
}

func TestJoinLog(t *testing.T) {
	if got := joinLog(nil); got != "" {
		t.Fatalf("nil: expected empty, got %q", got)
	}
	if got := joinLog([]string{"a"}); got != "a" {
		t.Fatalf("single: expected 'a', got %q", got)
	}
	if got := joinLog([]string{"a", "b", "c"}); got != "a → b → c" {
		t.Fatalf("multi: expected 'a → b → c', got %q", got)
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
	r := &CostRouter{strategy: model.StrategyCheapest}
	r.SetStrategy("unknown_strategy")
	if r.Strategy() != model.StrategyCheapest {
		t.Fatalf("unknown strategy should fall back to cheapest, got %s", r.Strategy())
	}
}

func TestCostRouter_SetStrategyEmpty(t *testing.T) {
	r := &CostRouter{strategy: model.StrategyBalanced}
	r.SetStrategy("")
	if r.Strategy() != model.StrategyCheapest {
		t.Fatalf("empty strategy should fall back to cheapest, got %s", r.Strategy())
	}
}
