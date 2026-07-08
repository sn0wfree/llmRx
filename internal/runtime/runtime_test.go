package runtime

import (
	"sync"
	"testing"
)

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

func TestBreakerMaxDefaults(t *testing.T) {
	d := New()
	if got := d.BreakerMaxFailures(); got != 5 {
		t.Fatalf("default: %d", got)
	}
	d.SetBreakerMaxFailures(42)
	if got := d.BreakerMaxFailures(); got != 42 {
		t.Fatalf("after set: %d", got)
	}
	d.SetBreakerMaxFailures(0)
	if got := d.BreakerMaxFailures(); got != 5 {
		t.Fatalf("zero should floor to 5, got %d", got)
	}
	d.SetBreakerMaxFailures(-3)
	if got := d.BreakerMaxFailures(); got != 5 {
		t.Fatalf("negative should floor to 5, got %d", got)
	}
}

func TestBreakerResetDefaults(t *testing.T) {
	d := New()
	if got := d.BreakerResetTimeoutMs(); got != 30000 {
		t.Fatalf("default: %d", got)
	}
	d.SetBreakerResetTimeoutMs(60000)
	if got := d.BreakerResetTimeoutMs(); got != 60000 {
		t.Fatalf("after set: %d", got)
	}
	d.SetBreakerResetTimeoutMs(50)
	if got := d.BreakerResetTimeoutMs(); got != 100 {
		t.Fatalf("below 100 should floor to 100, got %d", got)
	}
	d.SetBreakerResetTimeoutMs(48 * 60 * 60 * 1000)
	if got := d.BreakerResetTimeoutMs(); got != 24*60*60*1000 {
		t.Fatalf("above 24h should clamp to 24h, got %d", got)
	}
}

func TestAlertCooldownDefaults(t *testing.T) {
	d := New()
	if got := d.AlertCooldownSec(); got != 300 {
		t.Fatalf("default: %d", got)
	}
	d.SetAlertCooldownSec(120)
	if got := d.AlertCooldownSec(); got != 120 {
		t.Fatalf("after set: %d", got)
	}
	d.SetAlertCooldownSec(-1)
	if got := d.AlertCooldownSec(); got != 0 {
		t.Fatalf("negative should floor to 0, got %d", got)
	}
	d.SetAlertCooldownSec(48 * 60 * 60)
	if got := d.AlertCooldownSec(); got != 24*60*60 {
		t.Fatalf("above 24h should clamp to 24h, got %d", got)
	}
}

func TestLogRetentionDefaults(t *testing.T) {
	d := New()
	if got := d.LogRetentionDays(); got != 30 {
		t.Fatalf("default: %d", got)
	}
	d.SetLogRetentionDays(7)
	if got := d.LogRetentionDays(); got != 7 {
		t.Fatalf("after set: %d", got)
	}
	d.SetLogRetentionDays(0)
	if got := d.LogRetentionDays(); got != 0 {
		t.Fatalf("zero should remain 0 (disabled), got %d", got)
	}
	d.SetLogRetentionDays(5000)
	if got := d.LogRetentionDays(); got != 3650 {
		t.Fatalf("above 10y should clamp to 3650, got %d", got)
	}
}

// TestRuntimeConcurrent exercises every field with N concurrent
// readers and writers. The race detector (go test -race) is the
// real assertion; this test just keeps the workload running long
// enough for any data race to be observed.
func TestRuntimeConcurrent(t *testing.T) {
	d := New()
	const (
		readers = 8
		writers = 4
		ops     = 5000
	)
	var wg sync.WaitGroup
	wg.Add(readers + writers)
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				_ = d.MarkupRatio()
				_ = d.CostStrategy()
				_ = d.BreakerMaxFailures()
				_ = d.BreakerResetTimeoutMs()
				_ = d.AlertCooldownSec()
				_ = d.LogRetentionDays()
			}
		}()
	}
	for i := 0; i < writers; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				d.SetMarkupRatio(float64(j%10 + 1))
				d.SetCostStrategy("balanced")
				d.SetBreakerMaxFailures(int64((j + id) % 20))
				d.SetBreakerResetTimeoutMs(int64(j%1000 + 100))
				d.SetAlertCooldownSec(int64(j % 600))
				d.SetLogRetentionDays(int64(j % 100))
			}
		}(i)
	}
	wg.Wait()
}
