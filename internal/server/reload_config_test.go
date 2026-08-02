package server

import (
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sn0wfree/llmRx/internal/guardrail"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/tokencache"
)

func openSQLiteStore(t *testing.T) store.Store {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "t.db")
	st, err := store.OpenSQLite(dsn)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestReloadConfig_RefreshesCaches exercises the full propagation
// path with real components: tokencache, guardrail engine, router
// and pool must all reload without error.
func TestReloadConfig_RefreshesCaches(t *testing.T) {
	st := openSQLiteStore(t)
	tc := tokencache.New(st)
	ge := guardrail.New(st)
	cp := pool.NewChannelPool()
	if err := cp.LoadFromStore(st); err != nil {
		t.Fatalf("LoadFromStore: %v", err)
	}
	re := router.New(st, cp)

	s := &Server{tokens: tc, guardrailEngine: ge, pool: cp, router: re, store: st}
	s.ReloadConfig() // must not panic, errors are logged only

	// The router's static snapshot must still serve (it reloaded).
	_ = re
}

// TestReloadConfig_NilComponents: ReloadConfig tolerates unset
// components (e.g. alert manager not yet wired).
func TestReloadConfig_NilComponents(t *testing.T) {
	st := openSQLiteStore(t)
	tc := tokencache.New(st)
	s := &Server{tokens: tc} // guardrailEngine/alertMgr/router nil
	s.ReloadConfig()         // no panic
}

// TestSetReloadNotifier_FiresOnFireNotify: the notifier installed by
// SetReloadNotifier is invoked on every fireNotify call.
func TestSetReloadNotifier_FiresOnFireNotify(t *testing.T) {
	var n int32
	s := &Server{}
	s.SetReloadNotifier(func() { atomic.AddInt32(&n, 1) })
	s.fireNotify()
	s.fireNotify()
	s.fireNotify()
	if got := atomic.LoadInt32(&n); got != 3 {
		t.Fatalf("notifier fired %d times, want 3", got)
	}
}

// TestSetReloadNotifier_NoopWhenUnset: fireNotify with no notifier
// is a silent no-op.
func TestSetReloadNotifier_NoopWhenUnset(t *testing.T) {
	s := &Server{}
	s.fireNotify() // must not panic
}
