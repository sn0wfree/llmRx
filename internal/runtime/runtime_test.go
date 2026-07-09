package runtime

import (
	"encoding/json"
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

func TestSnapshotRoundtrip(t *testing.T) {
	d := New()
	d.SetCostStrategy("balanced")
	d.SetMarkupRatio(2.5)
	d.SetBreakerMaxFailures(7)
	d.SetBreakerResetTimeoutMs(45000)
	d.SetAlertCooldownSec(600)
	d.SetLogRetentionDays(14)
	d.SetStreamTimeoutSec(120)
	d.SetStreamMaxBodyBytes(16 * 1024 * 1024)
	d.SetMaxLogSubscribers(64)
	d.SetLogLevel(2)

	raw, err := d.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := Snapshot{
		CostStrategy:       "balanced",
		MarkupRatio:        2.5,
		BreakerMaxFailures: 7,
		BreakerResetMs:     45000,
		AlertCooldownSec:   600,
		LogRetentionDays:   14,
		StreamTimeoutSec:   120,
		StreamMaxBodyBytes: 16 * 1024 * 1024,
		MaxLogSubscribers:  64,
		LogLevel:           2,
	}
	if snap != want {
		t.Fatalf("snapshot roundtrip:\n got %+v\nwant %+v", snap, want)
	}

	// Apply to a fresh Defaults and confirm every field is restored.
	d2 := New()
	d2.Apply(snap)
	if d2.CostStrategy() != "balanced" {
		t.Fatalf("cost_strategy: got %q", d2.CostStrategy())
	}
	if got := d2.MarkupRatio(); got != 2.5 {
		t.Fatalf("markup: %v", got)
	}
	if got := d2.BreakerMaxFailures(); got != 7 {
		t.Fatalf("breaker max: %d", got)
	}
	if got := d2.BreakerResetTimeoutMs(); got != 45000 {
		t.Fatalf("breaker reset: %d", got)
	}
	if got := d2.AlertCooldownSec(); got != 600 {
		t.Fatalf("alert cooldown: %d", got)
	}
	if got := d2.LogRetentionDays(); got != 14 {
		t.Fatalf("retention: %d", got)
	}
	if got := d2.StreamTimeoutSec(); got != 120 {
		t.Fatalf("stream timeout: %d", got)
	}
	if got := d2.StreamMaxBodyBytes(); got != 16*1024*1024 {
		t.Fatalf("stream max body: %d", got)
	}
	if got := d2.MaxLogSubscribers(); got != 64 {
		t.Fatalf("max log subs: %d", got)
	}
	if got := d2.LogLevel(); got != 2 {
		t.Fatalf("log level: %d", got)
	}
}

func TestApplyIgnoresZero(t *testing.T) {
	// Apply with all zero / empty values should not clobber
	// fields where zero means "unset" (CostStrategy, MarkupRatio,
	// BreakerMax, BreakerResetMs). For fields where 0 is a
	// legitimate value (AlertCooldownSec=0 disables cooldown,
	// LogRetentionDays=0 disables retention), zero IS applied.
	d := New()
	d.SetCostStrategy("balanced")
	d.SetMarkupRatio(2.0)
	d.SetBreakerMaxFailures(20)
	d.SetBreakerResetTimeoutMs(45000)
	d.SetAlertCooldownSec(600)
	d.SetLogRetentionDays(14)

	d.Apply(Snapshot{}) // all zero / empty

	if got := d.CostStrategy(); got != "balanced" {
		t.Fatalf("cost_strategy: %q (should be preserved)", got)
	}
	if got := d.MarkupRatio(); got != 2.0 {
		t.Fatalf("markup: %v (should be preserved)", got)
	}
	if got := d.BreakerMaxFailures(); got != 20 {
		t.Fatalf("breaker max: %d (should be preserved)", got)
	}
	if got := d.BreakerResetTimeoutMs(); got != 45000 {
		t.Fatalf("breaker reset: %d (should be preserved)", got)
	}
	// 0 is a real value here, not a sentinel — applied.
	if got := d.AlertCooldownSec(); got != 0 {
		t.Fatalf("alert cooldown: %d (should be 0)", got)
	}
	if got := d.LogRetentionDays(); got != 0 {
		t.Fatalf("retention: %d (should be 0)", got)
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

// ---------- FormatLevel ----------

func TestFormatLevel(t *testing.T) {
	tests := []struct {
		level int64
		want  string
	}{
		{0, "debug: "},
		{1, ""},
		{2, "warn: "},
		{3, "error: "},
		{99, ""}, // unknown
		{-1, ""}, // unknown
	}
	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			if got := FormatLevel(tt.level); got != tt.want {
				t.Fatalf("FormatLevel(%d) = %q, want %q", tt.level, got, tt.want)
			}
		})
	}
}

// ---------- Stream/MaxLogSubscribers defaults ----------

func TestStreamTimeoutDefaults(t *testing.T) {
	d := New()
	// Default is 300s per New().
	if got := d.StreamTimeoutSec(); got != 300 {
		t.Fatalf("default: %d (want 300)", got)
	}
	d.SetStreamTimeoutSec(120)
	if got := d.StreamTimeoutSec(); got != 120 {
		t.Fatalf("after set: %d", got)
	}
}

func TestStreamMaxBodyBytesDefaults(t *testing.T) {
	d := New()
	// Default is 32 MiB per New().
	if got := d.StreamMaxBodyBytes(); got != 32*1024*1024 {
		t.Fatalf("default: %d (want %d)", got, 32*1024*1024)
	}
	d.SetStreamMaxBodyBytes(1024)
	if got := d.StreamMaxBodyBytes(); got != 1024 {
		t.Fatalf("after set: %d", got)
	}
}

func TestMaxLogSubscribersDefaults(t *testing.T) {
	d := New()
	if got := d.MaxLogSubscribers(); got != 0 {
		t.Fatalf("default: %d (want 0)", got)
	}
	d.SetMaxLogSubscribers(64)
	if got := d.MaxLogSubscribers(); got != 64 {
		t.Fatalf("after set: %d", got)
	}
}

func TestLogLevelDefaults(t *testing.T) {
	d := New()
	// Default is 0 (slog.LevelInfo per New() comment).
	if got := d.LogLevel(); got != 0 {
		t.Fatalf("default: %d (want 0)", got)
	}
	d.SetLogLevel(3)
	if got := d.LogLevel(); got != 3 {
		t.Fatalf("after set: %d", got)
	}
	// Negative clamps to 0
	d.SetLogLevel(-5)
	if got := d.LogLevel(); got != 0 {
		t.Fatalf("negative: %d (want 0)", got)
	}
}
