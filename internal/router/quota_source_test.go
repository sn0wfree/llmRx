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

// setVendorState writes a vendor report onto a channel and reloads the
// engine's static snapshot.
//
// UsageState is not part of UpdateChannel (it is monitor-owned state,
// written by UpdateChannelUsageState), so it has to go through that
// method — exactly as the real monitor does.
func setVendorState(t *testing.T, app *testhelper.App, id int64, probe model.UsageProbe, stateJSON string) {
	t.Helper()
	ch, err := app.Store.GetChannel(id)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}
	ch.UsageProbe = probe
	ch.PlanQuota = model.PlanQuota{
		Metric: model.QuotaMetricTokens, Limit: 1000, Window: model.QuotaWindowDaily,
	}
	if err := app.Store.UpdateChannel(ch); err != nil {
		t.Fatalf("update channel: %v", err)
	}
	if err := app.Store.UpdateChannelUsageState(id, stateJSON, time.Now(), ""); err != nil {
		t.Fatalf("update usage state: %v", err)
	}
	app.Engine.ReloadChannel(id)
}

// TestQuotaGuard_PrefersVendorData: the vendor report says the weekly
// window is spent even though the local count is low. The channel must
// be dropped — that is the whole point of P22.
func TestQuotaGuard_PrefersVendorData(t *testing.T) {
	app := testhelper.New(t)
	vendorBurned := app.AddChannelWithPrice("vendor-burned", "openai", "https://x", []string{"m"}, 1, 1, "k1")
	fresh := app.AddChannelWithPrice("fresh", "openai", "https://y", []string{"m"}, 1, 1, "k2")

	resets := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	state := `{"source":"openrouter_key","points":[{"window":"weekly","used":10,"limit":10,"remaining":0,"unit":"usd","resets_at":"` + resets + `"}]}`
	setVendorState(t, app, vendorBurned.ID, model.UsageProbe{Kind: model.UsageProbeOpenRouter}, state)
	// Local count is nearly zero: only the vendor data can save us.
	trk := usage.New()
	trk.Observe(vendorBurned.ID, 1, 0, 0, 0, 0, time.Now())
	trk.Observe(fresh.ID, 900, 0, 0, 0, 0, time.Now()) // fresh is locally "full"
	app.Engine.SetUsageTracker(trk)

	r, err := app.Engine.RouteWith(context.Background(), "m", router.RouteOptions{})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if r.Channel.ID != fresh.ID {
		t.Fatalf("vendor-exhausted channel still selected: %s", r.Channel.Name)
	}
	if !strings.Contains(r.RouterLog, "L2.5(quota,src)") {
		t.Fatalf("router log must mark vendor-sourced filtering: %q", r.RouterLog)
	}
}

// TestQuotaGuard_ExpiredVendorWindowFallsBack: once the vendor window's
// reset time has passed, the report is stale and the local ratio rules.
func TestQuotaGuard_ExpiredVendorWindowFallsBack(t *testing.T) {
	app := testhelper.New(t)
	ch := app.AddChannelWithPrice("ch", "openai", "https://x", []string{"m"}, 1, 1, "k1")

	resets := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	state := `{"source":"openrouter_key","points":[{"window":"weekly","used":10,"limit":10,"remaining":0,"unit":"usd","resets_at":"` + resets + `"}]}`
	setVendorState(t, app, ch.ID, model.UsageProbe{Kind: model.UsageProbeOpenRouter}, state)

	trk := usage.New()
	trk.Observe(ch.ID, 3, 0, 0, 0, 0, time.Now()) // 3/1000 locally
	app.Engine.SetUsageTracker(trk)

	if _, ok := trk.SourceRatio(ch, time.Now()); ok {
		t.Fatal("an expired vendor window must not be used")
	}
	r, err := app.Engine.RouteWith(context.Background(), "m", router.RouteOptions{})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if r.Channel.ID != ch.ID {
		t.Fatalf("channel should still route on local data, got %s", r.Channel.Name)
	}
	if strings.Contains(r.RouterLog, "L2.5(quota)") {
		t.Fatalf("guard should not have filtered: %q", r.RouterLog)
	}
}

// TestQuotaGuard_NoProbeFallsBackToLocal: channels without a probe
// behave exactly as they did in P21.
func TestQuotaGuard_NoProbeFallsBackToLocal(t *testing.T) {
	app := testhelper.New(t)
	burned := app.AddChannelWithPrice("burned", "openai", "https://x", []string{"m"}, 1, 1, "k1")
	fresh := app.AddChannelWithPrice("fresh", "openai", "https://y", []string{"m"}, 1, 1, "k2")
	for _, id := range []int64{burned.ID, fresh.ID} {
		chs, _ := app.Store.GetChannels()
		for i := range chs {
			if chs[i].ID == id {
				chs[i].PlanQuota = model.PlanQuota{
					Metric: model.QuotaMetricTokens, Limit: 100, Window: model.QuotaWindowDaily,
				}
				_ = app.Store.UpdateChannel(&chs[i])
			}
		}
		app.Engine.ReloadChannel(id)
	}

	trk := usage.New()
	trk.Observe(burned.ID, 500, 0, 0, 0, 0, time.Now())
	trk.Observe(fresh.ID, 10, 0, 0, 0, 0, time.Now())
	app.Engine.SetUsageTracker(trk)

	r, err := app.Engine.RouteWith(context.Background(), "m", router.RouteOptions{})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if r.Channel.ID != fresh.ID {
		t.Fatalf("local fallback failed: routed to %s", r.Channel.Name)
	}
	// Local filtering must NOT be labelled as vendor-sourced.
	if strings.Contains(r.RouterLog, "quota,src") {
		t.Fatalf("local filtering mislabelled: %q", r.RouterLog)
	}
	if !strings.Contains(r.RouterLog, "L2.5(quota)") {
		t.Fatalf("expected L2.5(quota): %q", r.RouterLog)
	}
}
