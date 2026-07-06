package router

import (
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func mk(name string, priority int, in, out float64) *model.Channel {
	return &model.Channel{Name: name, Priority: priority, InputPrice: in, OutputPrice: out, Status: model.ChannelEnabled}
}

func TestCostCheapest(t *testing.T) {
	r := &CostRouter{strategy: model.StrategyCheapest}
	in := []*model.Channel{
		mk("a", 1, 10, 10), // 20
		mk("b", 1, 1, 1),   // 2 (cheapest)
		mk("c", 1, 5, 5),   // 10
	}
	out := r.Sort(in)
	if out[0].Name != "b" {
		t.Fatalf("cheapest: expected b first, got %s", out[0].Name)
	}
	if out[len(out)-1].Name != "a" {
		t.Fatalf("cheapest: expected a last, got %s", out[len(out)-1].Name)
	}
}

func TestCostFastest(t *testing.T) {
	r := &CostRouter{strategy: model.StrategyFastest}
	in := []*model.Channel{
		mk("low", 1, 100, 100),
		mk("hi", 10, 100, 100),
		mk("mid", 5, 100, 100),
	}
	out := r.Sort(in)
	if out[0].Name != "hi" || out[1].Name != "mid" || out[2].Name != "low" {
		t.Fatalf("fastest: wrong order: %s, %s, %s", out[0].Name, out[1].Name, out[2].Name)
	}
}

func TestCostBalanced(t *testing.T) {
	r := &CostRouter{strategy: model.StrategyBalanced}
	in := []*model.Channel{
		mk("cheap-low-prio", 1, 1, 1),
		mk("cheap-hi-prio", 10, 1, 1),
		mk("expensive-low", 1, 50, 50),
		mk("expensive-hi", 10, 50, 50),
	}
	out := r.Sort(in)
	// Score = priceNorm*0.5 + (1-prioNorm)*0.5 — lower is better.
	// cheapest-hi-prio should win (low price, high priority → low score).
	if out[0].Name != "cheap-hi-prio" {
		t.Fatalf("balanced: expected cheap-hi-prio first, got %s", out[0].Name)
	}
}

func TestCostEmptyAndSingle(t *testing.T) {
	r := &CostRouter{strategy: model.StrategyCheapest}
	if got := r.Sort(nil); got != nil {
		t.Fatalf("empty: got %v", got)
	}
	if got := r.Sort([]*model.Channel{}); len(got) != 0 {
		t.Fatalf("empty slice: got %d", len(got))
	}
	single := []*model.Channel{mk("only", 1, 1, 1)}
	out := r.Sort(single)
	if len(out) != 1 || out[0].Name != "only" {
		t.Fatalf("single: got %v", out)
	}
}

func TestCostDoesNotMutateInput(t *testing.T) {
	r := &CostRouter{strategy: model.StrategyCheapest}
	in := []*model.Channel{
		mk("a", 1, 10, 10),
		mk("b", 1, 1, 1),
	}
	_ = r.Sort(in)
	if in[0].Name != "a" {
		t.Fatalf("Sort mutated input: in[0]=%s", in[0].Name)
	}
}

func TestCostStableForTies(t *testing.T) {
	r := &CostRouter{strategy: model.StrategyCheapest}
	in := []*model.Channel{
		mk("first", 1, 5, 5),
		mk("second", 1, 5, 5),
	}
	out := r.Sort(in)
	// sort.SliceStable preserves input order for equal keys.
	if out[0].Name != "first" || out[1].Name != "second" {
		t.Fatalf("stable sort broken: got %s, %s", out[0].Name, out[1].Name)
	}
}