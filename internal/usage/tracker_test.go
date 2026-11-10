package usage

import (
	"sync"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

func chWithQuota(id int64, q model.PlanQuota) *model.Channel {
	return &model.Channel{ID: id, Name: "c", PlanQuota: q}
}

// TestTokensMetric: quota metric "tokens" counts prompt+completion;
// reasoning is a subset of completion and must not double-count.
func TestTokensMetric(t *testing.T) {
	tr := New()
	ch := chWithQuota(1, model.PlanQuota{
		Metric: model.QuotaMetricTokens, Limit: 1000, Window: model.QuotaWindowMonthly,
	})
	now := time.Now()
	tr.Observe(1, 300, 200, 50, 100, 0.01, now)
	u := tr.Usage(ch, now)
	if u.Used != 500 {
		t.Fatalf("tokens used = %v, want 500 (prompt+completion, reasoning is a subset)", u.Used)
	}
	if u.Ratio != 0.5 {
		t.Fatalf("ratio = %v, want 0.5", u.Ratio)
	}
	if u.Prompt != 300 || u.Completion != 200 || u.Cached != 50 || u.Reasoning != 100 {
		t.Fatalf("split fields: %+v", u)
	}
	if u.RealUSD != 0.01 {
		t.Fatalf("real usd = %v", u.RealUSD)
	}
}

// TestPromptTokensMetric: the coding-plan-style metric counts prompt
// only (cached reads included).
func TestPromptTokensMetric(t *testing.T) {
	tr := New()
	ch := chWithQuota(1, model.PlanQuota{
		Metric: model.QuotaMetricPromptTokens, Limit: 400, Window: model.QuotaWindowDaily,
	})
	now := time.Now()
	tr.Observe(1, 300, 5000, 50, 0, 0, now)
	u := tr.Usage(ch, now)
	if u.Used != 300 {
		t.Fatalf("prompt_tokens used = %v, want 300", u.Used)
	}
	if u.Ratio != 0.75 {
		t.Fatalf("ratio = %v, want 0.75", u.Ratio)
	}
}

// TestUSDMetric: the usd metric accumulates real upstream cost.
func TestUSDMetric(t *testing.T) {
	tr := New()
	ch := chWithQuota(1, model.PlanQuota{
		Metric: model.QuotaMetricUSD, Limit: 10, Window: model.QuotaWindowMonthly,
	})
	now := time.Now()
	tr.Observe(1, 100, 100, 0, 0, 2.5, now)
	tr.Observe(1, 100, 100, 0, 0, 2.5, now)
	if u := tr.Usage(ch, now); u.Used != 5.0 || u.Ratio != 0.5 {
		t.Fatalf("usd used/ratio = %v/%v, want 5/0.5", u.Used, u.Ratio)
	}
}

// TestMonthlyWindowFlip: observations in the previous month must not
// leak into the current window.
func TestMonthlyWindowFlip(t *testing.T) {
	tr := New()
	ch := chWithQuota(1, model.PlanQuota{
		Metric: model.QuotaMetricTokens, Limit: 1000, Window: model.QuotaWindowMonthly,
	})
	now := time.Now().UTC()
	lastMonth := now.AddDate(0, -1, 0)
	tr.Observe(1, 900, 0, 0, 0, 0, lastMonth)
	// The channel's stored period key is last month's; current-month
	// usage must read as zero.
	u := tr.Usage(ch, now)
	if u.Used != 0 {
		t.Fatalf("stale month leaked: used = %v", u.Used)
	}
	// And the window bounds are the current month.
	start, end := WindowRange(ch.PlanQuota, now)
	if u.WindowStart != start || u.WindowEnd != end {
		t.Fatalf("window bounds = %v..%v, want %v..%v", u.WindowStart, u.WindowEnd, start, end)
	}
	if u.ResetAt != end {
		t.Fatalf("calendar windows reset at window end, got %v", u.ResetAt)
	}
}

// TestDailyWindow: same-day observations accumulate.
func TestDailyWindow(t *testing.T) {
	tr := New()
	ch := chWithQuota(1, model.PlanQuota{
		Metric: model.QuotaMetricTokens, Limit: 100, Window: model.QuotaWindowDaily,
	})
	// Anchor at noon UTC: the tracker keys calendar counters by UTC
	// day, so when this test runs in the first hour after midnight,
	// -1h lands on the previous day and legitimately resets them
	// (stale keys read as zero). Noon keeps both observations inside
	// one day on every run.
	base := time.Now().UTC()
	now := time.Date(base.Year(), base.Month(), base.Day(), 12, 0, 0, 0, time.UTC)
	tr.Observe(1, 10, 5, 0, 0, 0, now)
	tr.Observe(1, 10, 5, 0, 0, 0, now.Add(-time.Hour))
	if u := tr.Usage(ch, now); u.Used != 30 {
		t.Fatalf("daily used = %v, want 30", u.Used)
	}
}

// TestRollingWindow: only the trailing hours count; older buckets age
// out of the sum.
func TestRollingWindow(t *testing.T) {
	tr := New()
	ch := chWithQuota(1, model.PlanQuota{
		Metric:      model.QuotaMetricTokens,
		Limit:       100,
		Window:      model.QuotaWindowRolling,
		WindowHours: 5,
	})
	now := time.Now()
	tr.Observe(1, 40, 0, 0, 0, 0, now.Add(-6*time.Hour)) // outside the 5h window
	tr.Observe(1, 30, 0, 0, 0, 0, now.Add(-4*time.Hour)) // inside
	tr.Observe(1, 20, 0, 0, 0, 0, now)                   // inside
	u := tr.Usage(ch, now)
	if u.Used != 50 {
		t.Fatalf("rolling used = %v, want 50 (6h-old bucket excluded)", u.Used)
	}
	if !u.ResetAt.IsZero() {
		t.Fatalf("rolling windows slide; ResetAt should be zero, got %v", u.ResetAt)
	}
}

// TestRollingWindowRecovery: rows replayed oldest-first (the recovery
// order) end up in the same sum as live observation.
func TestRollingWindowRecovery(t *testing.T) {
	tr := New()
	ch := chWithQuota(1, model.PlanQuota{
		Metric:      model.QuotaMetricTokens,
		Limit:       100,
		Window:      model.QuotaWindowRolling,
		WindowHours: 5,
	})
	now := time.Now()
	tr.Recover([]*model.Log{
		{ChannelID: 1, PromptTokens: 40, CreatedAt: now.Add(-6 * time.Hour)},
		{ChannelID: 1, PromptTokens: 30, CreatedAt: now.Add(-4 * time.Hour)},
		{ChannelID: 1, PromptTokens: 20, CreatedAt: now},
	})
	if u := tr.Usage(ch, now); u.Used != 50 {
		t.Fatalf("recovered rolling used = %v, want 50", u.Used)
	}
}

// TestUntracked: channels without a quota read as ratio 0 and the
// router's fast path skips them.
func TestUntracked(t *testing.T) {
	tr := New()
	ch := &model.Channel{ID: 1}
	now := time.Now()
	tr.Observe(1, 500, 500, 0, 0, 1, now)
	if u := tr.Usage(ch, now); u.Ratio != 0 || u.Limit != 0 {
		t.Fatalf("untracked usage = %+v", u)
	}
	if r := tr.UsageRatio(ch, now); r != 0 {
		t.Fatalf("untracked ratio = %v", r)
	}
}

// TestExhaustedRatio: usage past the limit reports ratio > 1.
func TestExhaustedRatio(t *testing.T) {
	tr := New()
	ch := chWithQuota(1, model.PlanQuota{
		Metric: model.QuotaMetricTokens, Limit: 100, Window: model.QuotaWindowDaily,
	})
	now := time.Now()
	tr.Observe(1, 150, 0, 0, 0, 0, now)
	if r := tr.UsageRatio(ch, now); r < 1.4 || r > 1.6 {
		t.Fatalf("exhausted ratio = %v, want ~1.5", r)
	}
}

// TestObserveZeroChannel: rows without a channel are ignored.
func TestObserveZeroChannel(t *testing.T) {
	tr := New()
	tr.Observe(0, 100, 100, 0, 0, 1, time.Now())
	if len(tr.cal) != 0 {
		t.Fatalf("zero-channel observation created state: %v", tr.cal)
	}
}

// TestConcurrentAccess: Observe (emitter) and Usage (router) race
// freely; -race must stay quiet.
func TestConcurrentAccess(t *testing.T) {
	tr := New()
	ch := chWithQuota(1, model.PlanQuota{
		Metric: model.QuotaMetricTokens, Limit: 1 << 40, Window: model.QuotaWindowDaily,
	})
	now := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				tr.Observe(1, 10, 10, 0, 0, 0, now)
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = tr.UsageRatio(ch, now)
			}
		}()
	}
	wg.Wait()
	u := tr.Usage(ch, now)
	if u.Used != 8*200*20 {
		t.Fatalf("concurrent used = %v, want %d", u.Used, 8*200*20)
	}
}

// TestWindowRangeMonthly: monthly windows are UTC calendar months.
func TestWindowRangeMonthly(t *testing.T) {
	now := time.Date(2026, 11, 8, 15, 30, 0, 0, time.UTC)
	start, end := WindowRange(model.PlanQuota{Window: model.QuotaWindowMonthly}, now)
	if start != time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("monthly start = %v", start)
	}
	if end != time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("monthly end = %v", end)
	}
}

// TestWindowRangeRolling: rolling windows slide with now.
func TestWindowRangeRolling(t *testing.T) {
	now := time.Date(2026, 11, 8, 15, 30, 0, 0, time.UTC)
	start, end := WindowRange(model.PlanQuota{Window: model.QuotaWindowRolling, WindowHours: 5}, now)
	if end != now || start != now.Add(-5*time.Hour) {
		t.Fatalf("rolling range = %v..%v", start, end)
	}
}

// TestWindowRangeInvalid: unknown windows and untracked quotas return
// zero times.
func TestWindowRangeInvalid(t *testing.T) {
	now := time.Now()
	if s, _ := WindowRange(model.PlanQuota{}, now); !s.IsZero() {
		t.Fatalf("untracked range = %v", s)
	}
	if s, _ := WindowRange(model.PlanQuota{Window: "weekly", Limit: 1}, now); !s.IsZero() {
		t.Fatalf("unknown window range = %v", s)
	}
}

// TestSnapshot: Snapshot covers every channel keyed by ID.
func TestSnapshot(t *testing.T) {
	tr := New()
	now := time.Now()
	tr.Observe(1, 10, 0, 0, 0, 0, now)
	chs := []*model.Channel{
		chWithQuota(1, model.PlanQuota{Metric: model.QuotaMetricTokens, Limit: 100, Window: model.QuotaWindowDaily}),
		chWithQuota(2, model.PlanQuota{Metric: model.QuotaMetricUSD, Limit: 5, Window: model.QuotaWindowMonthly}),
	}
	snap := tr.Snapshot(chs, now)
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d", len(snap))
	}
	if snap[1].Used != 10 || snap[2].Used != 0 {
		t.Fatalf("snapshot values: %+v", snap)
	}
}

// TestPlanQuotaValidate: enum validation.
func TestPlanQuotaValidate(t *testing.T) {
	cases := []struct {
		q    model.PlanQuota
		want bool
	}{
		{model.PlanQuota{}, true},
		{model.PlanQuota{Metric: "tokens", Limit: 1, Window: "monthly"}, true},
		{model.PlanQuota{Kind: "code", Metric: "prompt_tokens", Limit: 1, Window: "rolling", WindowHours: 5}, true},
		{model.PlanQuota{Metric: "requests", Limit: 1, Window: "monthly"}, false},
		{model.PlanQuota{Metric: "tokens", Limit: 0, Window: "monthly"}, false},
		{model.PlanQuota{Metric: "tokens", Limit: 1, Window: "weekly"}, false},
		{model.PlanQuota{Metric: "tokens", Limit: 1, Window: "rolling", WindowHours: 0}, false},
		{model.PlanQuota{Metric: "tokens", Limit: 1, Window: "rolling", WindowHours: 10000}, false},
		{model.PlanQuota{Kind: "subscription", Metric: "tokens", Limit: 1, Window: "monthly"}, false},
	}
	for i, c := range cases {
		err := c.q.Validate()
		if (err == nil) != c.want {
			t.Fatalf("case %d (%+v): err = %v", i, c.q, err)
		}
	}
}
