package pool

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

func TestPool_RoundRobinSequential(t *testing.T) {
	p := NewChannelPool()
	p.channels[1] = &channelEntry{
		Channel: &model.Channel{ID: 1, Name: "c1"},
		Keys: []*keyEntry{
			{Key: "k1", Status: model.KeyActive},
			{Key: "k2", Status: model.KeyActive},
			{Key: "k3", Status: model.KeyActive},
		},
	}

	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		k, err := p.NextKey(1)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		seen[k.Key]++
	}
	// 6 requests across 3 keys → each returned exactly twice.
	for _, k := range []string{"k1", "k2", "k3"} {
		if seen[k] != 2 {
			t.Fatalf("key %s: expected 2 hits, got %d", k, seen[k])
		}
	}
}

func TestPool_SkipsInactiveKeys(t *testing.T) {
	p := NewChannelPool()
	p.channels[1] = &channelEntry{
		Channel: &model.Channel{ID: 1, Name: "c1"},
		Keys: []*keyEntry{
			{Key: "k1", Status: model.KeyRateLimited},
			{Key: "k2", Status: model.KeyDisabled},
			{Key: "k3", Status: model.KeyActive},
		},
	}

	for i := 0; i < 5; i++ {
		k, err := p.NextKey(1)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if k.Key != "k3" {
			t.Fatalf("expected only k3 to be returned, got %s", k.Key)
		}
	}
}

func TestPool_NoActiveKeyReturnsErr(t *testing.T) {
	p := NewChannelPool()
	p.channels[1] = &channelEntry{
		Channel: &model.Channel{ID: 1, Name: "c1"},
		Keys: []*keyEntry{
			{Key: "k1", Status: model.KeyDisabled},
			{Key: "k2", Status: model.KeyRateLimited},
		},
	}

	_, err := p.NextKey(1)
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("expected ErrNoKey, got %v", err)
	}
}

func TestPool_EmptyChannelReturnsErr(t *testing.T) {
	p := NewChannelPool()
	p.channels[1] = &channelEntry{
		Channel: &model.Channel{ID: 1, Name: "c1"},
		Keys:    []*keyEntry{},
	}
	_, err := p.NextKey(1)
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("expected ErrNoKey, got %v", err)
	}
}

func TestPool_UnknownChannelReturnsErr(t *testing.T) {
	p := NewChannelPool()
	_, err := p.NextKey(42)
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("expected ErrNoKey, got %v", err)
	}
}

func TestPool_MaskedKeyFormat(t *testing.T) {
	p := NewChannelPool()
	p.channels[1] = &channelEntry{
		Channel: &model.Channel{ID: 1, Name: "c1"},
		Keys: []*keyEntry{
			{Key: "sk-abcdefghijklmnop", Status: model.KeyActive},
		},
	}
	k, _ := p.NextKey(1)
	if k.KeyMasked != "sk-a***mnop" {
		t.Fatalf("expected sk-a***mnop, got %s", k.KeyMasked)
	}
}

func TestPool_UpsertReplacesChannel(t *testing.T) {
	p := NewChannelPool()
	p.UpsertChannel(
		&model.Channel{ID: 7, Name: "x"},
		[]model.Key{{Key: "k1", Status: model.KeyActive}},
	)
	k, err := p.NextKey(7)
	if err != nil || k.Key != "k1" {
		t.Fatalf("Upsert: got key=%s err=%v", k.Key, err)
	}

	p.RemoveChannel(7)
	if _, err := p.NextKey(7); !errors.Is(err, ErrNoKey) {
		t.Fatalf("RemoveChannel: expected ErrNoKey, got %v", err)
	}
}

// ---------- LoadFromStore ----------

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")
	s, err := store.OpenSQLite(dsn)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	logDir := filepath.Join(dir, "logs")
	if err := logstore.EnsureDir(logDir); err != nil {
		t.Fatalf("logstore.EnsureDir: %v", err)
	}
	logStore, err := logstore.New(logDir, nil)
	if err != nil {
		t.Fatalf("logstore.New: %v", err)
	}
	s.SetLogStore(logStore)
	t.Cleanup(func() { _ = logStore.Close() })

	return s
}

func TestPool_LoadFromStore(t *testing.T) {
	p := NewChannelPool()
	s := newTestStore(t)

	// Create enabled channel with keys
	ch1 := &model.Channel{
		Name: "ch1", Provider: "openai", BaseURL: "https://api.openai.com",
		Status: model.ChannelEnabled,
	}
	if err := s.CreateChannel(ch1); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if err := s.CreateKey(&model.Key{ChannelID: ch1.ID, Key: "k1", KeyMasked: "k***1", Status: model.KeyActive}); err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if err := s.CreateKey(&model.Key{ChannelID: ch1.ID, Key: "k2", KeyMasked: "k***2", Status: model.KeyActive}); err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	if err := p.LoadFromStore(s); err != nil {
		t.Fatalf("LoadFromStore: %v", err)
	}

	// Verify channel was loaded
	k, err := p.NextKey(ch1.ID)
	if err != nil {
		t.Fatalf("NextKey: %v", err)
	}
	if k.Key != "k1" && k.Key != "k2" {
		t.Fatalf("unexpected key: %s", k.Key)
	}
}

func TestPool_LoadFromStoreSkipsDisabled(t *testing.T) {
	p := NewChannelPool()
	s := newTestStore(t)

	// Create disabled channel
	ch := &model.Channel{
		Name: "disabled", Provider: "openai", BaseURL: "https://api.openai.com",
		Status: model.ChannelDisabled,
	}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	if err := p.LoadFromStore(s); err != nil {
		t.Fatalf("LoadFromStore: %v", err)
	}

	// Should not be loaded
	if _, err := p.NextKey(ch.ID); !errors.Is(err, ErrNoKey) {
		t.Fatalf("expected ErrNoKey for disabled channel, got %v", err)
	}
}

func TestPool_LoadFromStoreReplacesExisting(t *testing.T) {
	p := NewChannelPool()
	s := newTestStore(t)

	// Load once with no channels
	if err := p.LoadFromStore(s); err != nil {
		t.Fatalf("LoadFromStore (empty): %v", err)
	}

	// Add a channel
	ch := &model.Channel{
		Name: "new", Provider: "openai", BaseURL: "https://api.openai.com",
		Status: model.ChannelEnabled,
	}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if err := s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "k1", KeyMasked: "k***1", Status: model.KeyActive}); err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	// Reload
	if err := p.LoadFromStore(s); err != nil {
		t.Fatalf("LoadFromStore: %v", err)
	}

	k, err := p.NextKey(ch.ID)
	if err != nil {
		t.Fatalf("NextKey: %v", err)
	}
	if k.Key != "k1" {
		t.Fatalf("expected k1, got %s", k.Key)
	}
}

// ---------- GetAllChannels ----------

func TestPool_GetAllChannels(t *testing.T) {
	p := NewChannelPool()
	s := newTestStore(t)

	// Create multiple channels
	for i := 0; i < 3; i++ {
		ch := &model.Channel{
			Name: "ch" + string(rune('a'+i)), Provider: "openai",
			BaseURL: "https://api.openai.com", Status: model.ChannelEnabled,
		}
		if err := s.CreateChannel(ch); err != nil {
			t.Fatalf("CreateChannel %d: %v", i, err)
		}
	}

	if err := p.LoadFromStore(s); err != nil {
		t.Fatalf("LoadFromStore: %v", err)
	}

	channels := p.GetAllChannels()
	if len(channels) != 3 {
		t.Fatalf("expected 3 channels, got %d", len(channels))
	}
}

func TestPool_GetAllChannelsEmpty(t *testing.T) {
	p := NewChannelPool()
	channels := p.GetAllChannels()
	if len(channels) != 0 {
		t.Fatalf("expected 0 channels, got %d", len(channels))
	}
}

// ---------- Concurrency ----------

func TestPool_ConcurrentNextKey(t *testing.T) {
	p := NewChannelPool()
	p.channels[1] = &channelEntry{
		Channel: &model.Channel{ID: 1, Name: "c1"},
		Keys: []*keyEntry{
			{Key: "k1", Status: model.KeyActive},
			{Key: "k2", Status: model.KeyActive},
			{Key: "k3", Status: model.KeyActive},
		},
	}

	const goroutines = 10
	const perGoroutine = 100
	var wg sync.WaitGroup
	var errCount int
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				_, err := p.NextKey(1)
				if err != nil {
					errCount++
				}
			}
		}()
	}
	wg.Wait()
	if errCount > 0 {
		t.Fatalf("concurrent NextKey had %d errors", errCount)
	}
}

// ---------- UpsertChannel edge cases ----------

func TestPool_UpsertChannelEmptyKeys(t *testing.T) {
	p := NewChannelPool()
	p.UpsertChannel(&model.Channel{ID: 5, Name: "empty"}, []model.Key{})
	_, err := p.NextKey(5)
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("expected ErrNoKey for empty keys, got %v", err)
	}
}

func TestPool_UpsertChannelOverwrites(t *testing.T) {
	p := NewChannelPool()
	// Initial insert
	p.UpsertChannel(
		&model.Channel{ID: 3, Name: "v1"},
		[]model.Key{{Key: "old", Status: model.KeyActive}},
	)
	// Overwrite with new key
	p.UpsertChannel(
		&model.Channel{ID: 3, Name: "v2"},
		[]model.Key{{Key: "new", Status: model.KeyActive}},
	)
	k, err := p.NextKey(3)
	if err != nil {
		t.Fatalf("NextKey: %v", err)
	}
	if k.Key != "new" {
		t.Fatalf("expected 'new', got %s", k.Key)
	}
}

// ---------- RemoveChannel edge cases ----------

func TestPool_RemoveNonExistentChannel(t *testing.T) {
	p := NewChannelPool()
	// Should not panic
	p.RemoveChannel(999)
}

func TestPool_GetAllChannelsAfterRemove(t *testing.T) {
	p := NewChannelPool()
	p.UpsertChannel(&model.Channel{ID: 1, Name: "c1"}, []model.Key{{Key: "k1", Status: model.KeyActive}})
	p.UpsertChannel(&model.Channel{ID: 2, Name: "c2"}, []model.Key{{Key: "k2", Status: model.KeyActive}})

	if len(p.GetAllChannels()) != 2 {
		t.Fatal("expected 2 channels")
	}
	p.RemoveChannel(1)
	if len(p.GetAllChannels()) != 1 {
		t.Fatal("expected 1 channel after remove")
	}
}
