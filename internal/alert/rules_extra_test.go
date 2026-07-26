package alert

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

func TestEvaluate_KeyExhausted(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.OpenSQLite(filepath.Join(dir, "test.db"))
	t.Cleanup(func() { _ = s.Close() })

	ch := &model.Channel{Name: "ch", Provider: "openai", Protocol: "openai", BaseURL: "https://x", Models: []string{"m"}, Status: model.ChannelEnabled}
	s.CreateChannel(ch)

	r := &model.Alert{Type: model.AlertKeyExhausted, Threshold: 0, WindowSec: 300}
	fired, _, err := Evaluate(r, time.Now(), s)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fired {
		t.Errorf("key_exhausted should fire when channel has no keys")
	}
}

func TestEvaluate_KeyExhausted_WithKeys(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.OpenSQLite(filepath.Join(dir, "test.db"))
	t.Cleanup(func() { _ = s.Close() })

	ch := &model.Channel{Name: "ch", Provider: "openai", Protocol: "openai", BaseURL: "https://x", Models: []string{"m"}, Status: model.ChannelEnabled}
	s.CreateChannel(ch)
	s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "sk-test", KeyMasked: "sk-t***test", Status: model.KeyActive})

	r := &model.Alert{Type: model.AlertKeyExhausted, Threshold: 0, WindowSec: 300}
	fired, _, err := Evaluate(r, time.Now(), s)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fired {
		t.Errorf("key_exhausted should not fire when channel has keys")
	}
}

func TestEvaluate_UnknownType(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.OpenSQLite(filepath.Join(dir, "test.db"))
	t.Cleanup(func() { _ = s.Close() })

	r := &model.Alert{Type: "unknown_type", Threshold: 0, WindowSec: 300}
	_, _, err := Evaluate(r, time.Now(), s)
	if err == nil {
		t.Errorf("unknown type should return error")
	}
}

func TestManager_StartWithAlerts(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.OpenSQLite(filepath.Join(dir, "test.db"))
	t.Cleanup(func() { _ = s.Close() })

	a := &model.Alert{Name: "a", Type: model.AlertCostSpike, Threshold: 100, WindowSec: 300, CooldownSec: 300, Enabled: true}
	s.CreateAlert(a)

	mgr := NewManager(s, nil, Config{})
	mgr.Reload()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Start(ctx)
	time.Sleep(100 * time.Millisecond)
}

func TestManager_Cooldown(t *testing.T) {
	mgr := &Manager{}
	_ = mgr
}
