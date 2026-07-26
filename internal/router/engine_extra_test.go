package router_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sn0wfree/llmRx/internal/intent"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestRouter_RouteNoChannel(t *testing.T) {
	app := testhelper.New(t)
	_, err := app.Engine.Route(context.Background(), "nonexistent-model")
	if err == nil {
		t.Fatal("expected error for no matching channel")
	}
}

func TestRouter_RouteSingleChannel(t *testing.T) {
	app := testhelper.New(t)
	ch := app.AddChannelWithPrice("c1", "openai", "https://x", []string{"m"}, 1, 1, "k1")
	r, err := app.Engine.Route(context.Background(), "m")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if r.Channel.ID != ch.ID {
		t.Fatalf("expected channel %d, got %d", ch.ID, r.Channel.ID)
	}
	if r.KeyValue != "k1" {
		t.Fatalf("expected key k1, got %s", r.KeyValue)
	}
	if r.RouterLog == "" {
		t.Fatal("RouterLog should not be empty")
	}
}

func TestRouter_RouteAllBroken(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannelWithPrice("c1", "openai", "https://x", []string{"m"}, 1, 1, "k1")
	chs, _ := app.Store.GetChannels()
	for i := 0; i < 6; i++ {
		app.Engine.RecordFailure(chs[0].ID)
	}
	_, err := app.Engine.Route(context.Background(), "m")
	if err == nil {
		t.Fatal("expected error when all channels are broken")
	}
}

func TestRouter_RouteNoKey(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("nokey-chan", "openai", "https://x", []string{"m"})
	_, err := app.Engine.Route(context.Background(), "m")
	if err == nil {
		t.Fatal("expected error when channel has no keys")
	}
}

func TestRouter_CostStrategy(t *testing.T) {
	app := testhelper.New(t)
	if app.Engine.CostStrategy() != model.StrategyCheapest {
		t.Fatalf("default strategy should be cheapest, got %s", app.Engine.CostStrategy())
	}
	app.Engine.SetStrategy(model.StrategyBalanced)
	if app.Engine.CostStrategy() != model.StrategyBalanced {
		t.Fatalf("expected balanced, got %s", app.Engine.CostStrategy())
	}
	app.Engine.SetStrategy(model.StrategyFastest)
	if app.Engine.CostStrategy() != model.StrategyFastest {
		t.Fatalf("expected fastest, got %s", app.Engine.CostStrategy())
	}
}

func TestRouter_SetStrategyAffectsRouting(t *testing.T) {
	app := testhelper.New(t)
	ch1 := app.AddChannelWithPrice("cheap", "openai", "https://x", []string{"m"}, 1, 1, "k1")
	ch2 := app.AddChannelWithPrice("expensive", "openai", "https://y", []string{"m"}, 10, 10, "k2")
	ch1.Priority = 1
	ch2.Priority = 10
	app.Store.UpdateChannel(ch1)
	app.Store.UpdateChannel(ch2)

	app.Engine.SetStrategy(model.StrategyCheapest)
	r, _ := app.Engine.Route(context.Background(), "m")
	if r.Channel.ID != ch1.ID {
		t.Fatalf("cheapest: expected %s, got %s", ch1.Name, r.Channel.Name)
	}

	app.Engine.SetStrategy(model.StrategyFastest)
	r, _ = app.Engine.Route(context.Background(), "m")
	if r.Channel.ID != ch2.ID {
		t.Fatalf("fastest: expected %s, got %s", ch2.Name, r.Channel.Name)
	}
}

func TestRouter_IntentBackend(t *testing.T) {
	app := testhelper.New(t)
	if app.Engine.IntentBackend() != "disabled" {
		t.Fatalf("default backend should be 'disabled', got %s", app.Engine.IntentBackend())
	}
	app.Engine.SetIntentClassifier(intent.Nop{})
	if app.Engine.IntentBackend() != "disabled" {
		t.Fatalf("Nop backend should be 'disabled', got %s", app.Engine.IntentBackend())
	}
	stub := &stubClassifier{kind: "code"}
	app.Engine.SetIntentClassifier(stub)
	if app.Engine.IntentBackend() != "stub" {
		t.Fatalf("stub backend should be 'stub', got %s", app.Engine.IntentBackend())
	}
}

func TestRouter_SetIntentClassifierNil(t *testing.T) {
	app := testhelper.New(t)
	app.Engine.SetIntentClassifier(nil)
	if app.Engine.IntentBackend() != "disabled" {
		t.Fatalf("nil classifier should fall back to 'disabled', got %s", app.Engine.IntentBackend())
	}
}

func TestRouter_RecordSuccess(t *testing.T) {
	app := testhelper.New(t)
	ch := app.AddChannelWithPrice("c", "openai", "https://x", []string{"m"}, 1, 1, "k")
	app.Engine.RecordSuccess(ch.ID)
	snap := app.Engine.Thompson().Snapshot()
	if ab, ok := snap[ch.ID]; !ok || ab[0] < 2.0 {
		t.Fatalf("RecordSuccess should increment alpha: %+v", snap)
	}
}

func TestRouter_RecordFailure(t *testing.T) {
	app := testhelper.New(t)
	ch := app.AddChannelWithPrice("c", "openai", "https://x", []string{"m"}, 1, 1, "k")
	app.Engine.RecordFailure(ch.ID)
	snap := app.Engine.Thompson().Snapshot()
	if ab, ok := snap[ch.ID]; !ok || ab[1] < 2.0 {
		t.Fatalf("RecordFailure should increment beta: %+v", snap)
	}
}

func TestRouter_ThompsonAccessor(t *testing.T) {
	app := testhelper.New(t)
	if app.Engine.Thompson() == nil {
		t.Fatal("Thompson() should not return nil")
	}
}

func TestRouter_SetBreakerDefaults(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannelWithPrice("c", "openai", "https://x", []string{"m"}, 1, 1, "k")
	chs, _ := app.Store.GetChannels()
	live := &liveDefaultsWrapper{maxFailures: 2, resetMs: 30}
	app.Engine.SetBreakerDefaults(live)
	app.Engine.RecordFailure(chs[0].ID)
	app.Engine.RecordFailure(chs[0].ID)
	r, err := app.Engine.Route(context.Background(), "m")
	if err == nil {
		t.Fatalf("expected breaker to be open after 2 failures with live defaults, got channel %s", r.Channel.Name)
	}
}

func TestRouter_LoadSaveThompsonState(t *testing.T) {
	app := testhelper.New(t)
	ch := app.AddChannelWithPrice("c", "openai", "https://x", []string{"m"}, 1, 1, "k")
	app.Engine.RecordSuccess(ch.ID)
	app.Engine.RecordSuccess(ch.ID)
	app.Engine.RecordFailure(ch.ID)

	statePath := filepath.Join(t.TempDir(), "thompson_state.json")
	if err := app.Engine.SaveThompsonState(statePath); err != nil {
		t.Fatalf("SaveThompsonState: %v", err)
	}
	snap1 := app.Engine.Thompson().Snapshot()

	app.Engine.ReloadAllChannels()
	snap2 := app.Engine.Thompson().Snapshot()
	if _, ok := snap2[ch.ID]; ok {
		t.Fatal("ReloadAllChannels should clear Thompson state")
	}

	if err := app.Engine.LoadThompsonState(statePath); err != nil {
		t.Fatalf("LoadThompsonState: %v", err)
	}
	snap3 := app.Engine.Thompson().Snapshot()
	ab, ok := snap3[ch.ID]
	if !ok {
		t.Fatal("LoadThompsonState should restore channel state")
	}
	ab1 := snap1[ch.ID]
	if ab[0] != ab1[0] || ab[1] != ab1[1] {
		t.Fatalf("state mismatch after load: got %v, want %v", ab, ab1)
	}
}

func TestRouter_LoadThompsonStateMissingFile(t *testing.T) {
	app := testhelper.New(t)
	missingPath := filepath.Join(t.TempDir(), "nonexistent.json")
	if err := app.Engine.LoadThompsonState(missingPath); err != nil {
		t.Fatalf("LoadThompsonState with missing file should be a no-op, got %v", err)
	}
}

func TestRouter_ReloadChannelClearsThompson(t *testing.T) {
	app := testhelper.New(t)
	ch := app.AddChannelWithPrice("c", "openai", "https://x", []string{"m"}, 1, 1, "k")
	app.Engine.RecordSuccess(ch.ID)
	if app.Engine.Thompson().Snapshot()[ch.ID][0] < 2.0 {
		t.Fatal("RecordSuccess should have updated posterior")
	}
	app.Engine.ReloadChannel(ch.ID)
	if _, ok := app.Engine.Thompson().Snapshot()[ch.ID]; ok {
		t.Fatal("ReloadChannel should clear Thompson state for the channel")
	}
}

func TestRouter_RouteWithLogsRouterPath(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannelWithPrice("c1", "openai", "https://x", []string{"m"}, 1, 1, "k1")
	r, _ := app.Engine.RouteWith(context.Background(), "m", router.RouteOptions{})
	if r.RouterLog == "" {
		t.Fatal("RouterLog should be populated")
	}
}

func TestRouter_RegisterExtraChannelsInvoked(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannelWithPrice("main", "openai", "https://x", []string{"m"}, 1, 1, "k1")
	called := false
	app.Engine.RegisterExtraChannels(func() []*model.Channel {
		called = true
		return nil
	})
	app.Engine.Route(context.Background(), "m")
	if !called {
		t.Fatal("extra channels callback should be invoked during Route")
	}
}

type liveDefaultsWrapper struct {
	maxFailures int64
	resetMs     int64
}

func (l *liveDefaultsWrapper) BreakerMaxFailures() int64   { return l.maxFailures }
func (l *liveDefaultsWrapper) BreakerResetTimeoutMs() int64 { return l.resetMs }
