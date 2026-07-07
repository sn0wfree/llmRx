package router_test

import (
	"context"
	"testing"

	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// TestRouter_ReloadAllClearsBreakerState verifies that
// ReloadAllChannels drops every channel's breaker state so a
// subsequent Route() call returns all healthy channels.
func TestRouter_ReloadAllClearsBreakerState(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannelWithPrice("c1", "openai", "https://x", []string{"m"}, 1, 1, "k1")
	app.AddChannelWithPrice("c2", "openai", "https://y", []string{"m"}, 2, 2, "k2")
	// Default MaxFailures is 5; trip both breakers via repeated
	// failures (use direct breaker access since we need many calls).
	chs, _ := app.Store.GetChannels()
	for _, c := range chs {
		for i := 0; i < 6; i++ {
			app.Engine.RecordFailure(c.ID)
		}
	}
	if r, _ := app.Engine.RouteWith(context.Background(), "m", router.RouteOptions{}); r != nil {
		t.Fatalf("expected no available channel after tripping both, got %v", r)
	}
	// ReloadAllChannels should clear breaker state.
	app.Engine.ReloadAllChannels()
	if r, _ := app.Engine.RouteWith(context.Background(), "m", router.RouteOptions{}); r == nil {
		t.Fatal("after ReloadAll, expected a channel")
	}
}

// Sanity: ReloadChannel (existing) still works after the new
// ReloadAllChannels was added.
func TestRouter_ReloadChannelStillWorks(t *testing.T) {
	app := testhelper.New(t)
	ch := app.AddChannelWithPrice("c", "openai", "https://x", []string{"m"}, 1, 1, "k")
	for i := 0; i < 6; i++ {
		app.Engine.RecordFailure(ch.ID)
	}
	if r, _ := app.Engine.RouteWith(context.Background(), "m", router.RouteOptions{}); r != nil {
		t.Fatalf("breaker should be open, got %v", r)
	}
	app.Engine.ReloadChannel(ch.ID)
	if r, _ := app.Engine.RouteWith(context.Background(), "m", router.RouteOptions{}); r == nil {
		t.Fatal("after ReloadChannel, channel should be healthy")
	}
	_ = router.RouteOptions{} // keep the package referenced
}