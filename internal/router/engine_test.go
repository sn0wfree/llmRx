package router_test

import (
	"context"
	"testing"

	"github.com/sn0wfree/llmRx/internal/intent"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// L4 with a Nop classifier should never reorder candidates.
func TestRouter_L4NopDoesNothing(t *testing.T) {
	app := testhelper.New(t)
	app.Engine.SetIntentClassifier(intent.Nop{})
	ch1 := app.AddChannelWithPrice("ch1", "openai", "https://x", []string{"m"}, 1, 1, "k1")
	app.AddChannelWithPrice("ch2", "openai", "https://y", []string{"m"}, 2, 2, "k2")
	r, _ := app.Engine.RouteWith(context.Background(), "m", router.RouteOptions{Text: "def hello(): return 42"})
	if r.Channel.ID != ch1.ID {
		t.Fatalf("expected ch1, got %s", r.Channel.Name)
	}
}

// L4 with a custom classifier that reports "code" should bubble the
// channel whose Intents includes "code" to the front, even if it's
// more expensive.
func TestRouter_L4IntentReorders(t *testing.T) {
	app := testhelper.New(t)
	// Two channels with equal priority/cost; ch_code declares the
	// "code" intent, ch_other does not. The default L3 will keep
	// insertion order, so L4 is the only thing that can move
	// ch_code to the front.
	app.AddChannelWithPrice("ch_code", "openai", "https://x", []string{"m"}, 1, 1, "k1")
	chOther := app.AddChannelWithPrice("ch_other", "openai", "https://y", []string{"m"}, 1, 1, "k2")
	_ = chOther
	// Mark ch_code with the "code" intent. The store mutates the
	// channel in place via the engine; we patch via the store.
	chList, _ := app.Store.GetChannels()
	for i := range chList {
		if chList[i].Name == "ch_code" {
			chList[i].Intents = []string{"code"}
			if err := app.Store.UpdateChannel(&chList[i]); err != nil {
				t.Fatalf("update: %v", err)
			}
		}
	}

	// Stub classifier that always reports "code".
	stub := &stubClassifier{kind: "code"}
	app.Engine.SetIntentClassifier(stub)

	r, _ := app.Engine.RouteWith(context.Background(), "m", router.RouteOptions{Text: "anything"})
	if r.Channel.Name != "ch_code" {
		t.Fatalf("expected ch_code after L4, got %s", r.Channel.Name)
	}
	if r.Intent.Kind != "code" {
		t.Fatalf("intent not propagated: %+v", r.Intent)
	}
}

type stubClassifier struct {
	kind string
}

func (s *stubClassifier) Classify(_ string) intent.Intent {
	return intent.Intent{Kind: s.kind, Score: 0.9}
}
func (s *stubClassifier) Backend() string { return "stub" }
func (s *stubClassifier) Close() error    { return nil }
