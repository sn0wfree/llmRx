package router_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/sn0wfree/llmRx/internal/intent"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// countingClassifier counts every Classify call. The intent stage
// must skip it when there's only one candidate (the plain path
// hit per request before the short-circuit landed in stage.go).
type countingClassifier struct {
	calls int64
	kind  string
}

func (c *countingClassifier) Classify(_ string) intent.Intent {
	atomic.AddInt64(&c.calls, 1)
	return intent.Intent{Kind: c.kind, Score: 0.9}
}
func (c *countingClassifier) Backend() string { return "counting" }
func (c *countingClassifier) Close() error   { return nil }

// TestRouter_L4SkipsSingleCandidate verifies the short-circuit
// added to intentStage.Apply: when L1 already produced a single
// candidate, the cgo call is skipped because the result can have
// no observable effect (no reordering, no filtering).
func TestRouter_L4SkipsSingleCandidate(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannelWithPrice("only", "openai", "https://x", []string{"m"}, 1, 1, "k1")
	cc := &countingClassifier{kind: "code"}
	app.Engine.SetIntentClassifier(cc)

	for i := 0; i < 100; i++ {
		_, err := app.Engine.RouteWith(context.Background(), "m", router.RouteOptions{Text: "anything"})
		if err != nil {
			t.Fatalf("route: %v", err)
		}
	}
	if got := atomic.LoadInt64(&cc.calls); got != 0 {
		t.Fatalf("expected 0 cgo calls with single candidate, got %d", got)
	}
}

// TestRouter_L4RunsWithMultipleCandidates is the guard: when more
// than one candidate is in play, L4 must still fire so it can
// reorder by intent.
func TestRouter_L4RunsWithMultipleCandidates(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannelWithPrice("ch1", "openai", "https://x", []string{"m"}, 1, 1, "k1")
	app.AddChannelWithPrice("ch2", "openai", "https://y", []string{"m"}, 1, 1, "k2")
	cc := &countingClassifier{kind: "general"}
	app.Engine.SetIntentClassifier(cc)

	for i := 0; i < 50; i++ {
		_, err := app.Engine.RouteWith(context.Background(), "m", router.RouteOptions{Text: "anything"})
		if err != nil {
			t.Fatalf("route: %v", err)
		}
	}
	if got := atomic.LoadInt64(&cc.calls); got != 50 {
		t.Fatalf("expected 50 cgo calls with multiple candidates, got %d", got)
	}
}
