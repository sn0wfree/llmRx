package alert

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

func openTestStore(t *testing.T) store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.OpenSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	logDir := filepath.Join(dir, "logs")
	if err := logstore.EnsureDir(logDir); err != nil {
		t.Fatalf("logstore.EnsureDir: %v", err)
	}
	ls, _ := logstore.New(logDir, nil)
	s.SetLogStore(ls)
	t.Cleanup(func() { _ = ls.Close() })
	return s
}

func TestNewManager_Defaults(t *testing.T) {
	st := openTestStore(t)
	m := NewManager(st, nil, Config{})
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	if m.tickInterval != 30*time.Second {
		t.Fatalf("default tickInterval: got %v", m.tickInterval)
	}
	if m.fallbackCooldown != 5*time.Minute {
		t.Fatalf("default cooldown: got %v", m.fallbackCooldown)
	}
}

func TestNewManager_CustomConfig(t *testing.T) {
	st := openTestStore(t)
	m := NewManager(st, nil, Config{
		TickInterval:    10 * time.Second,
		DefaultCooldown: 2 * time.Minute,
	})
	if m.tickInterval != 10*time.Second {
		t.Fatalf("custom tickInterval: got %v", m.tickInterval)
	}
	if m.fallbackCooldown != 2*time.Minute {
		t.Fatalf("custom cooldown: got %v", m.fallbackCooldown)
	}
}

func TestManager_Reload(t *testing.T) {
	st := openTestStore(t)
	st.CreateAlert(&model.Alert{Name: "a", Type: model.AlertErrorRate, Threshold: 0.5, Enabled: true})
	m := NewManager(st, nil, Config{})
	if err := m.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(m.rules))
	}
}

func TestManager_Start(t *testing.T) {
	st := openTestStore(t)
	m := NewManager(st, nil, Config{TickInterval: 50 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	go m.Start(ctx)
	time.Sleep(150 * time.Millisecond)
	cancel()
}

func TestManager_Cooldown_Fallback(t *testing.T) {
	st := openTestStore(t)
	m := NewManager(st, nil, Config{DefaultCooldown: 3 * time.Minute})
	if got := m.cooldown(); got != 3*time.Minute {
		t.Fatalf("fallback cooldown: got %v", got)
	}
}
