package cache

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/sn0wfree/llmRx/internal/provider"
)

func openSQLiteCache(t testing.TB) *SQLiteCache {
	t.Helper()
	dir, err := os.MkdirTemp("", "cache-sqlite-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	db, err := sql.Open("sqlite3", dir+"/cache.db?_journal=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	c, err := NewSQLiteCache(db)
	if err != nil {
		t.Fatalf("NewSQLiteCache: %v", err)
	}
	return c
}

func TestSQLiteCache_GetMiss(t *testing.T) {
	c := openSQLiteCache(t)
	_, ok, err := c.Get(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatal("expected miss")
	}
}

func TestSQLiteCache_SetGet(t *testing.T) {
	c := openSQLiteCache(t)
	e := &Entry{
		Key:        "test-key",
		StatusCode: 200,
		Body:       json.RawMessage(`{"choices":[{"message":{"content":"hello"}}]}`),
		Usage:      &provider.Usage{PromptTokens: 10, CompletionTokens: 20},
		CostUSD:    0.001,
		ChannelID:  1,
	}
	if err := c.Set(context.Background(), e, 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, err := c.Get(context.Background(), "test-key")
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

func TestSQLiteCache_TTLExpiry(t *testing.T) {
	c := openSQLiteCache(t)
	e := &Entry{Key: "ttl-key", StatusCode: 200, Body: json.RawMessage(`{}`)}
	if err := c.Set(context.Background(), e, 2*time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}
	_, ok, _ := c.Get(context.Background(), "ttl-key")
	if !ok {
		t.Fatal("expected hit before TTL expiry")
	}

	// Manually set expires_at to 1 second ago so we don't have to wait.
	_, _ = c.db.Exec("UPDATE response_cache SET expires_at = ? WHERE key = 'ttl-key'", time.Now().Unix()-1)

	_, ok, _ = c.Get(context.Background(), "ttl-key")
	if ok {
		t.Fatal("expected miss after TTL expiry")
	}
}

func TestSQLiteCache_Delete(t *testing.T) {
	c := openSQLiteCache(t)
	_ = c.Set(context.Background(), &Entry{Key: "del-key", StatusCode: 200, Body: json.RawMessage(`{}`)}, 0)
	_ = c.Delete(context.Background(), "del-key")
	_, ok, _ := c.Get(context.Background(), "del-key")
	if ok {
		t.Fatal("expected miss after delete")
	}
}

func TestSQLiteCache_Purge(t *testing.T) {
	c := openSQLiteCache(t)
	_ = c.Set(context.Background(), &Entry{Key: "k1", StatusCode: 200, Body: json.RawMessage(`{}`)}, 0)
	_ = c.Set(context.Background(), &Entry{Key: "k2", StatusCode: 200, Body: json.RawMessage(`{}`)}, 0)
	_ = c.Purge(context.Background())
	stats, _ := c.Stats(context.Background())
	if stats.Size != 0 {
		t.Fatalf("expected 0 after purge, got %d", stats.Size)
	}
}

func TestSQLiteCache_PersistenceAcrossOpen(t *testing.T) {
	dir, _ := os.MkdirTemp("", "cache-sqlite-persist-*")
	defer os.RemoveAll(dir)

	db1, _ := sql.Open("sqlite3", dir+"/cache.db?_journal=WAL&_busy_timeout=5000")
	c1, _ := NewSQLiteCache(db1)
	_ = c1.Set(context.Background(), &Entry{Key: "persist-key", StatusCode: 200, Body: json.RawMessage(`{"ok":true}`)}, 0)
	_ = c1.Close()
	db1.Close()

	db2, _ := sql.Open("sqlite3", dir+"/cache.db?_journal=WAL&_busy_timeout=5000")
	c2, _ := NewSQLiteCache(db2)
	defer c2.Close()
	defer db2.Close()

	got, ok, err := c2.Get(context.Background(), "persist-key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected hit across reopen")
	}
	if string(got.Body) != `{"ok":true}` {
		t.Fatalf("expected body {\"ok\":true}, got %s", string(got.Body))
	}
}

func TestSQLiteCache_ConcurrentAccess(t *testing.T) {
	c := openSQLiteCache(t)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := string(rune('a' + (n % 26)))
			_ = c.Set(context.Background(), &Entry{Key: key, StatusCode: 200, Body: json.RawMessage(`{}`)}, 0)
			_, _, _ = c.Get(context.Background(), key)
			_ = c.Delete(context.Background(), key)
		}(i)
	}
	wg.Wait()
}

func TestSQLiteCache_GzipRoundTrip(t *testing.T) {
	c := openSQLiteCache(t)
	body := json.RawMessage(`{"choices":[{"message":{"content":"` + string(make([]byte, 1000)) + `"}}]}`)
	e := &Entry{Key: "gzip-key", StatusCode: 200, Body: body}
	if err := c.Set(context.Background(), e, 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, _ := c.Get(context.Background(), "gzip-key")
	if !ok {
		t.Fatal("expected hit")
	}
	if string(got.Body) != string(body) {
		t.Fatal("body mismatch after gzip round-trip")
	}
}
