package router_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/testhelper"
	"github.com/sn0wfree/llmRx/internal/usage"
)

// setQuota attaches a plan quota to a channel and refreshes the
// engine's static snapshot so the next route sees it.
func setQuota(t *testing.T, app *testhelper.App, id int64, q model.PlanQuota) {
	t.Helper()
	chs, err := app.Store.GetChannels()
	if err != nil {
		t.Fatalf("get channels: %v", err)
	}
	for i := range chs {
		if chs[i].ID == id {
			chs[i].PlanQuota = q
			if err := app.Store.UpdateChannel(&chs[i]); err != nil {
				t.Fatalf("update channel: %v", err)
			}
		}
	}
	app.Engine.ReloadChannel(id)
}

func quotaTrackedQuota() model.PlanQuota {
	return model.PlanQuota{
		Kind: "code", Metric: model.QuotaMetricTokens,
		Limit: 100, Window: model.QuotaWindowDaily,
	}
}

// TestQuotaGuard_DropsExhausted: a channel past 100% of its quota
// window is removed from the candidates; traffic lands on the healthy
// one and the router log records the L2.5 filter.
func TestQuotaGuard_DropsExhausted(t *testing.T) {
	app := testhelper.New(t)
	// Equal prices: the static index (insertion order) puts burned
	// first, so only the quota guard can move traffic to fresh.
	burned := app.AddChannelWithPrice("burned", "openai", "https://x", []string{"m"}, 1, 1, "k1")
	fresh := app.AddChannelWithPrice("fresh", "openai", "https://y", []string{"m"}, 1, 1, "k2")
	setQuota(t, app, burned.ID, quotaTrackedQuota())
	setQuota(t, app, fresh.ID, quotaTrackedQuota())

	trk := usage.New()
	now := time.Now()
	// burned: 150/100 used → ratio 1.5; fresh: 10/100.
	trk.Observe(burned.ID, 150, 0, 0, 0, 0, now)
	trk.Observe(fresh.ID, 10, 0, 0, 0, 0, now)
	app.Engine.SetUsageTracker(trk)

	r, err := app.Engine.RouteWith(context.Background(), "m", router.RouteOptions{})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if r.Channel.ID != fresh.ID {
		t.Fatalf("expected fresh channel, routed to %s", r.Channel.Name)
	}
	if !strings.Contains(r.RouterLog, "L2.5(quota)") {
		t.Fatalf("router log missing L2.5 marker: %q", r.RouterLog)
	}
}

// TestQuotaGuard_AllExhaustedFallback: when every candidate is
// exhausted the request still routes (an upstream throttle beats a
// hard routing failure).
func TestQuotaGuard_AllExhaustedFallback(t *testing.T) {
	app := testhelper.New(t)
	only := app.AddChannelWithPrice("only", "openai", "https://x", []string{"m"}, 1, 1, "k1")
	setQuota(t, app, only.ID, quotaTrackedQuota())

	trk := usage.New()
	trk.Observe(only.ID, 500, 0, 0, 0, 0, time.Now())
	app.Engine.SetUsageTracker(trk)

	r, err := app.Engine.RouteWith(context.Background(), "m", router.RouteOptions{})
	if err != nil {
		t.Fatalf("all-exhausted must still route: %v", err)
	}
	if r.Channel.ID != only.ID {
		t.Fatalf("expected the exhausted channel as fallback, got %s", r.Channel.Name)
	}
}

// TestQuotaGuard_DisabledByRuntimeFlag: the live toggle turns the
// guard off entirely.
func TestQuotaGuard_DisabledByRuntimeFlag(t *testing.T) {
	app := testhelper.New(t)
	// Equal prices: the static index (insertion order) decides, so
	// burned is the would-be winner and only the guard can move it.
	burned := app.AddChannelWithPrice("burned", "openai", "https://x", []string{"m"}, 1, 1, "k1")
	fresh := app.AddChannelWithPrice("fresh", "openai", "https://y", []string{"m"}, 1, 1, "k2")
	setQuota(t, app, burned.ID, quotaTrackedQuota())
	setQuota(t, app, fresh.ID, quotaTrackedQuota())

	trk := usage.New()
	trk.Observe(burned.ID, 150, 0, 0, 0, 0, time.Now())
	trk.Observe(fresh.ID, 10, 0, 0, 0, 0, time.Now())
	app.Engine.SetUsageTracker(trk)
	app.Engine.SetQuotaGuardEnabledFn(func() bool { return false })
	defer app.Engine.SetQuotaGuardEnabledFn(nil)

	r, err := app.Engine.RouteWith(context.Background(), "m", router.RouteOptions{})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if r.Channel.ID != burned.ID {
		t.Fatalf("guard disabled but channel still avoided: routed to %s", r.Channel.Name)
	}
}

// TestQuotaGuard_UntrackedUnaffected: channels without a quota never
// trip the guard even with heavy usage.
func TestQuotaGuard_UntrackedUnaffected(t *testing.T) {
	app := testhelper.New(t)
	big := app.AddChannelWithPrice("big", "openai", "https://x", []string{"m"}, 1, 1, "k1")
	app.AddChannelWithPrice("other", "openai", "https://y", []string{"m"}, 1, 1, "k2")
	// big intentionally left untracked.

	trk := usage.New()
	trk.Observe(big.ID, 10_000_000, 0, 0, 0, 0, time.Now())
	app.Engine.SetUsageTracker(trk)

	r, err := app.Engine.RouteWith(context.Background(), "m", router.RouteOptions{})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if r.Channel.ID != big.ID {
		t.Fatalf("untracked channel was deprioritised: routed to %s", r.Channel.Name)
	}
}

// stubUsage is a router.UsageProvider with a fixed per-channel
// consumed-ratio map (router_test package counterpart of the internal
// test fake).
type stubUsage struct {
	ratio map[int64]float64
}

func (f *stubUsage) UsageRatio(ch *model.Channel, now time.Time) float64 { return f.ratio[ch.ID] }

// TestQuotaAwareStrategy_PrefersQuotaLeft: at equal price the channel
// with more quota left wins; the exhausted one ranks last.
func TestQuotaAwareStrategy_PrefersQuotaLeft(t *testing.T) {
	up := &stubUsage{ratio: map[int64]float64{
		1: 0.2, // plenty left
		2: 0.9, // past the soft line
		3: 1.5, // exhausted
	}}
	s := router.QuotaAwareStrategy{Provider: up, SoftThreshold: 0.8}
	chs := []*model.Channel{
		{ID: 2, Name: "low", InputPrice: 1, OutputPrice: 1, PlanQuota: quotaTrackedQuota()},
		{ID: 1, Name: "fresh", InputPrice: 1, OutputPrice: 1, PlanQuota: quotaTrackedQuota()},
		{ID: 3, Name: "dead", InputPrice: 1, OutputPrice: 1, PlanQuota: quotaTrackedQuota()},
	}
	out := s.Sort(chs)
	if out[0].ID != 1 {
		t.Fatalf("fresh should win, got %s", out[0].Name)
	}
	if out[len(out)-1].ID != 3 {
		t.Fatalf("exhausted should rank last, got %s", out[len(out)-1].Name)
	}
}

// TestQuotaAwareStrategy_PreferFlatRateOverPayg: a half-used flat-rate
// plan channel (tracked, ratio 0.5 → pressure 0.5) ties an untracked
// pay-as-you-go one (neutral 0.5) — the stable sort keeps input order,
// so the plan channel placed first stays first.
func TestQuotaAwareStrategy_NeutralUntracked(t *testing.T) {
	up := &stubUsage{ratio: map[int64]float64{1: 0.5}}
	s := router.QuotaAwareStrategy{Provider: up}
	plan := &model.Channel{ID: 1, Name: "plan", InputPrice: 1, OutputPrice: 1, PlanQuota: quotaTrackedQuota()}
	payg := &model.Channel{ID: 2, Name: "payg", InputPrice: 1, OutputPrice: 1}
	out := s.Sort([]*model.Channel{plan, payg})
	if out[0].ID != 1 || out[1].ID != 2 {
		t.Fatalf("neutral pressure must be a stable tie, got %s,%s", out[0].Name, out[1].Name)
	}
}
