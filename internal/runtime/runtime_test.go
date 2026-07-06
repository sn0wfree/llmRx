package runtime

import "testing"

func TestMarkupDefaults(t *testing.T) {
	d := New()
	if got := d.MarkupRatio(); got != 1.0 {
		t.Fatalf("default: %v", got)
	}
	d.SetMarkupRatio(2.5)
	if got := d.MarkupRatio(); got != 2.5 {
		t.Fatalf("after set: %v", got)
	}
}

func TestMarkupFloor(t *testing.T) {
	d := New()
	d.SetMarkupRatio(0)
	if got := d.MarkupRatio(); got != 1.0 {
		t.Fatalf("zero should floor to 1.0, got %v", got)
	}
	d.SetMarkupRatio(-5)
	if got := d.MarkupRatio(); got != 1.0 {
		t.Fatalf("negative should floor to 1.0, got %v", got)
	}
}

func TestCostStrategyDefault(t *testing.T) {
	d := New()
	if got := d.CostStrategy(); got != "cheapest" {
		t.Fatalf("default: %q", got)
	}
	d.SetCostStrategy("balanced")
	if got := d.CostStrategy(); got != "balanced" {
		t.Fatalf("after set: %q", got)
	}
}

func TestCostStrategyEmptyFloor(t *testing.T) {
	d := New()
	d.SetCostStrategy("")
	if got := d.CostStrategy(); got != "cheapest" {
		t.Fatalf("empty should floor to cheapest, got %q", got)
	}
}
