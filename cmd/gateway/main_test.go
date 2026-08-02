package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
	"github.com/sn0wfree/llmRx/internal/store"
)

func openTestStore(t *testing.T) store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestMaskKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"short", "short"},
		{"12345678", "12345678"},
		{"123456789", "1234***6789"},
		{"sk-abcdefghij123456", "sk-a***3456"},
	}
	for _, tc := range tests {
		if got := secrets.Mask(tc.in); got != tc.want {
			t.Errorf("secrets.Mask(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestFirstRune(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"hello", "h"},
		{"世界", "世"},
		{"a", "a"},
	}
	for _, tc := range tests {
		if got := firstRune(tc.in); got != tc.want {
			t.Errorf("firstRune(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestLastRune(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"hello", "o"},
		{"世界", "界"},
		{"a", "a"},
	}
	for _, tc := range tests {
		if got := lastRune(tc.in); got != tc.want {
			t.Errorf("lastRune(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestSeed_Success(t *testing.T) {
	st := openTestStore(t)
	cfg := &config.Config{}
	cfg.Server.AdminPassword = "testpass123"
	cfg.Server.AllowDefaultAdminPassword = true
	cfg.Channels = []config.ChannelConfig{
		{Name: "ch1", Provider: "openai", BaseURL: "https://x", Models: []string{"gpt-4"}, Keys: []string{"sk-test1234567890"}},
	}
	if err := seed(st, cfg); err != nil {
		t.Fatalf("seed: %v", err)
	}
	u, _ := st.GetUserByUsername("admin")
	if u == nil {
		t.Fatal("admin should be seeded")
	}
	chs, _ := st.GetChannels()
	if len(chs) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(chs))
	}
	keys, _ := st.GetKeys(chs[0].ID)
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
	if keys[0].KeyMasked == keys[0].Key {
		t.Error("key should be masked")
	}
}

func TestSeed_Idempotent(t *testing.T) {
	st := openTestStore(t)
	cfg := &config.Config{}
	cfg.Server.AllowDefaultAdminPassword = true
	if err := seed(st, cfg); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if err := seed(st, cfg); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	chs, _ := st.GetChannels()
	if len(chs) != 0 {
		t.Errorf("second seed should be no-op, got %d channels", len(chs))
	}
}

func TestSeedChannels_DefaultProtocol(t *testing.T) {
	st := openTestStore(t)
	cfg := &config.Config{}
	cfg.Channels = []config.ChannelConfig{
		{Name: "ch1", Provider: "openai", BaseURL: "https://x", Models: []string{"m"}},
	}
	if err := seedChannels(st, cfg); err != nil {
		t.Fatalf("seedChannels: %v", err)
	}
	chs, _ := st.GetChannels()
	if len(chs) != 1 || chs[0].Protocol != "openai" {
		t.Errorf("protocol should default to openai, got %+v", chs)
	}
}

func TestSeedChannels_Idempotent(t *testing.T) {
	st := openTestStore(t)
	cfg := &config.Config{}
	cfg.Channels = []config.ChannelConfig{
		{Name: "ch1", Provider: "openai", BaseURL: "https://x", Models: []string{"m"}},
	}
	seedChannels(st, cfg)
	seedChannels(st, cfg)
	chs, _ := st.GetChannels()
	if len(chs) != 1 {
		t.Errorf("expected 1 channel (idempotent), got %d", len(chs))
	}
}

func TestCleanupLoop_ExitsOnCancel(t *testing.T) {
	st := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		cleanupLoop(ctx, st)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanupLoop did not exit within 2s")
	}
}

func TestMaybeWriteStarterConfig_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := maybeWriteStarterConfig(dir, cfgPath); err != nil {
		t.Fatalf("maybeWriteStarterConfig: %v", err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !contains(string(data), "8787") {
		t.Errorf("config should contain port 8787")
	}
	if !contains(string(data), dir+"/llmrx.db") {
		t.Errorf("config should contain data dir DSN")
	}
}

func TestMaybeWriteStarterConfig_Idempotent(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	maybeWriteStarterConfig(dir, cfgPath)
	err := maybeWriteStarterConfig(dir, cfgPath)
	if err != nil {
		t.Errorf("second call should be no-op: %v", err)
	}
}

func TestChownRecursive_NonRootNoop(t *testing.T) {
	dir := t.TempDir()
	if err := chownRecursive(dir, 1000, 1000); err != nil {
		t.Errorf("chownRecursive as non-root should not error: %v", err)
	}
}

func TestChownIfRoot_NonRootNoop(t *testing.T) {
	if err := chownIfRoot("/tmp/test", "nobody"); err != nil {
		t.Errorf("chownIfRoot as non-root should not error: %v", err)
	}
}

func TestDropPrivileges_NonRootNoop(t *testing.T) {
	if err := dropPrivileges("nobody"); err != nil {
		t.Errorf("dropPrivileges as non-root should not error: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

var _ = model.ChannelEnabled
