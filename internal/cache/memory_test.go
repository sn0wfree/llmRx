package cache

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/provider"
)

func TestMemoryCache_GetMiss(t *testing.T) {
	m := NewMemoryCache(100)
	_, ok, err := m.Get(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatal("expected miss for nonexistent key")
	}
}

func TestMemoryCache_SetGet(t *testing.T) {
	m := NewMemoryCache(100)
	e := &Entry{
		Key:        "test-key",
		StatusCode: 200,
		Body:       json.RawMessage(`{"choices":[{"message":{"content":"hello"}}]}`),
		Usage:      &provider.Usage{PromptTokens: 10, CompletionTokens: 20},
		CostUSD:    0.001,
		ChannelID:  1,
	}
	if err := m.Set(context.Background(), e, 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, err := m.Get(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected hit")
	}
	if got.StatusCode != 200 {
		t.Fatalf("expected status 200, got %d", got.StatusCode)
	}
	if got.Usage.PromptTokens != 10 {
		t.Fatalf("expected prompt_tokens=10, got %d", got.Usage.PromptTokens)
	}
}

func TestMemoryCache_TTLExpiry(t *testing.T) {
	m := NewMemoryCache(100)
	e := &Entry{Key: "ttl-key", StatusCode: 200, Body: json.RawMessage(`{}`)}
	if err := m.Set(context.Background(), e, 50*time.Millisecond); err != nil {
		t.Fatalf("Set: %v", err)
	}
	_, ok, _ := m.Get(context.Background(), "ttl-key")
	if !ok {
		t.Fatal("expected hit before TTL expiry")
	}
	time.Sleep(100 * time.Millisecond)
	_, ok, _ = m.Get(context.Background(), "ttl-key")
	if ok {
		t.Fatal("expected miss after TTL expiry")
	}
}

func TestMemoryCache_LRUEviction(t *testing.T) {
	m := NewMemoryCache(3)
	for i := 0; i < 4; i++ {
		key := string(rune('a' + i))
		_ = m.Set(context.Background(), &Entry{Key: key, StatusCode: 200, Body: json.RawMessage(`{}`)}, 0)
	}
	_, ok, _ := m.Get(context.Background(), "a")
	if ok {
		t.Fatal("expected 'a' to be evicted (LRU)")
	}
	_, ok, _ = m.Get(context.Background(), "d")
	if !ok {
		t.Fatal("expected 'd' to be present (most recent)")
	}
}

func TestMemoryCache_LRURefresh(t *testing.T) {
	m := NewMemoryCache(3)
	for i := 0; i < 3; i++ {
		key := string(rune('a' + i))
		_ = m.Set(context.Background(), &Entry{Key: key, StatusCode: 200, Body: json.RawMessage(`{}`)}, 0)
	}
	_, ok, _ := m.Get(context.Background(), "a")
	if !ok {
		t.Fatal("expected 'a' to be present")
	}
	_ = m.Set(context.Background(), &Entry{Key: "d", StatusCode: 200, Body: json.RawMessage(`{}`)}, 0)
	_, ok, _ = m.Get(context.Background(), "b")
	if ok {
		t.Fatal("expected 'b' to be evicted (a was refreshed)")
	}
	_, ok, _ = m.Get(context.Background(), "a")
	if !ok {
		t.Fatal("expected 'a' to be present (refreshed)")
	}
}

func TestMemoryCache_Delete(t *testing.T) {
	m := NewMemoryCache(100)
	_ = m.Set(context.Background(), &Entry{Key: "del-key", StatusCode: 200, Body: json.RawMessage(`{}`)}, 0)
	_ = m.Delete(context.Background(), "del-key")
	_, ok, _ := m.Get(context.Background(), "del-key")
	if ok {
		t.Fatal("expected miss after delete")
	}
}

func TestMemoryCache_Purge(t *testing.T) {
	m := NewMemoryCache(100)
	_ = m.Set(context.Background(), &Entry{Key: "k1", StatusCode: 200, Body: json.RawMessage(`{}`)}, 0)
	_ = m.Set(context.Background(), &Entry{Key: "k2", StatusCode: 200, Body: json.RawMessage(`{}`)}, 0)
	_ = m.Purge(context.Background())
	stats, _ := m.Stats(context.Background())
	if stats.Size != 0 {
		t.Fatalf("expected size 0 after purge, got %d", stats.Size)
	}
}

func TestMemoryCache_Stats(t *testing.T) {
	m := NewMemoryCache(100)
	stats, _ := m.Stats(context.Background())
	if stats.Hits != 0 || stats.Misses != 0 || stats.Size != 0 {
		t.Fatalf("expected empty stats, got %+v", stats)
	}
	_ = m.Set(context.Background(), &Entry{Key: "k", StatusCode: 200, Body: json.RawMessage(`{}`)}, 0)
	_, _, _ = m.Get(context.Background(), "k")
	_, _, _ = m.Get(context.Background(), "missing")
	stats, _ = m.Stats(context.Background())
	if stats.Hits != 1 || stats.Misses != 1 || stats.Size != 1 {
		t.Fatalf("expected hits=1 misses=1 size=1, got %+v", stats)
	}
	if stats.HitRate != 0.5 {
		t.Fatalf("expected hit_rate=0.5, got %f", stats.HitRate)
	}
}

func TestMemoryCache_ConcurrentAccess(t *testing.T) {
	m := NewMemoryCache(1000)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := string(rune('a' + (n % 26)))
			_ = m.Set(context.Background(), &Entry{Key: key, StatusCode: 200, Body: json.RawMessage(`{}`)}, 0)
			_, _, _ = m.Get(context.Background(), key)
		_ = m.Delete(context.Background(), key)
		}(i)
	}
	wg.Wait()
}

func TestMemoryCache_Close(t *testing.T) {
	m := NewMemoryCache(100)
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}