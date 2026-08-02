package router_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// BenchmarkRoute_Match verifies the static L1 cache after seeding
// 50 channels each with 5 models. Before the cache, this called
// store.GetChannels() on every Route(); after the cache, it's a
// snapshot pointer read.
func BenchmarkRoute_Match(b *testing.B) {
	// testhelper.New requires a *testing.T, but we can use
	// b as one indirectly via a stub. Easiest: build via a real T.
	stubT := &testing.T{}
	app := testhelper.New(stubT)
	for i := 0; i < 50; i++ {
		app.AddChannel(fmt.Sprintf("ch%d", i), "openai", "https://x",
			[]string{"m0", "m1", "m2", "m3", "m4"}, "k")
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = app.Engine.Route(ctx, "m2")
	}
}
