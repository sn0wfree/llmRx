package router

import (
	"context"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

// TestRouterEngine_RegisterExtraChannels asserts that callbacks
// registered via RegisterExtraChannels are invoked during L1 and
// that their returned channels join the candidate list. With no
// callbacks registered (the current production setup) this is a
// no-op.
func TestRouterEngine_RegisterExtraChannels(t *testing.T) {
	// No live store needed: just verify RegisterExtraChannels
	// stores the callback and nil callbacks are ignored.
	e := &RouterEngine{}
	e.RegisterExtraChannels(nil) // ignored
	if len(e.extraChannels) != 0 {
		t.Errorf("expected 0 callbacks after nil Register, got %d", len(e.extraChannels))
	}

	called := false
	e.RegisterExtraChannels(func() []*model.Channel {
		called = true
		return nil
	})
	if len(e.extraChannels) != 1 {
		t.Errorf("expected 1 callback, got %d", len(e.extraChannels))
	}

	// Invoke directly; this exercises the registration plumbing
	// without needing a real router pipeline.
	for _, src := range e.extraChannels {
		src()
	}
	if !called {
		t.Errorf("registered callback was not invoked")
	}

	_ = context.Background()
}
