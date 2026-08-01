package logstore

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- Async insert basics ----------

func TestAsyncInsertEventuallyPersisted(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()

	now := time.Now()
	m.Insert(makeLog(1, 1, "m", 200, now))
	m.Insert(makeLog(1, 1, "m", 200, now))
	m.Flush()

	rows, total, err := m.Query(QueryFilter{}, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 2 {
		t.Errorf("total=%d want 2", total)
	}
	if len(rows) != 2 {
		t.Errorf("rows=%d want 2", len(rows))
	}
}

// ---------- Flush drains all pending entries ----------

func TestAsyncFlushDrainsAll(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()

	const n = 100
	now := time.Now()
	for i := 0; i < n; i++ {
		_ = m.Insert(makeLog(1, 1, "m", 200, now))
	}
	m.Flush()

	_, total, _ := m.Query(QueryFilter{}, nil)
	if total != int64(n) {
		t.Errorf("total=%d want %d", total, n)
	}
}

// ---------- Close drains before shutting down ----------

func TestAsyncCloseDrains(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	now := time.Now()
	m.Insert(makeLog(1, 1, "m", 200, now))
	_ = m.Close() // should flush before closing

	// Re-open to verify entries were persisted.
	m2, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m2.Close()
	m2.SetAsyncConfig(AsyncConfig{Enabled: false})

	_, total, _ := m2.Query(QueryFilter{}, nil)
	if total != 1 {
		t.Errorf("total=%d want 1 (Close should drain)", total)
	}
}

// ---------- Sync fallback when channel is full ----------

func TestAsyncFallbackOnFullChannel(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()

	// Set a tiny channel so it fills up immediately.
	m.SetAsyncConfig(AsyncConfig{
		Enabled:       true,
		BatchSize:     500,
		FlushInterval: time.Hour,
		ChannelSize:   1,
	})

	// The first insert fills the channel; subsequent inserts
	// fall back to sync. All should succeed.
	now := time.Now()
	for i := 0; i < 10; i++ {
		if err := m.Insert(makeLog(1, 1, "m", 200, now)); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	m.Flush()

	_, total, _ := m.Query(QueryFilter{}, nil)
	if total != 10 {
		t.Errorf("total=%d want 10", total)
	}
}

// ---------- Concurrent inserts ----------

func TestAsyncConcurrentInserts(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()

	const workers = 10
	const insertsPerWorker = 100
	var wg sync.WaitGroup
	now := time.Now()

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < insertsPerWorker; i++ {
				_ = m.Insert(makeLog(1, 1, "m", 200, now))
			}
		}()
	}
	wg.Wait()
	m.Flush()

	_, total, _ := m.Query(QueryFilter{}, nil)
	expected := int64(workers * insertsPerWorker)
	if total != expected {
		t.Errorf("total=%d want %d", total, expected)
	}
}

// ---------- Async off: Insert goes directly to driver ----------

func TestAsyncDisabled(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()
	m.SetAsyncConfig(AsyncConfig{Enabled: false})

	now := time.Now()
	m.Insert(makeLog(1, 1, "m", 200, now))
	m.Insert(makeLog(1, 1, "m", 200, now))

	rows, total, _ := m.Query(QueryFilter{}, nil)
	if total != 2 {
		t.Errorf("total=%d want 2", total)
	}
	if len(rows) != 2 {
		t.Errorf("rows=%d want 2", len(rows))
	}
}

// ---------- Benchmark: async vs sync ----------

func BenchmarkAsyncInsert(b *testing.B) {
	dir := b.TempDir()
	m, err := New(dir, nil)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	defer m.Close()

	entry := makeLog(1, 1, "m", 200, time.Now())
	b.ResetTimer()

	var count int64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = m.Insert(entry)
			atomic.AddInt64(&count, 1)
		}
	})
	m.Flush()
}

func BenchmarkSyncInsert(b *testing.B) {
	dir := b.TempDir()
	m, err := New(dir, nil)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	defer m.Close()
	m.SetAsyncConfig(AsyncConfig{Enabled: false})

	entry := makeLog(1, 1, "m", 200, time.Now())
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = m.Insert(entry)
		}
	})
}