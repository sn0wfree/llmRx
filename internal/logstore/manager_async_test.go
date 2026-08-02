package logstore

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
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

// TestWorkerPanicRestarts: after the batch worker panics it must be
// restarted with fresh channels, and concurrent Insert/Flush must
// keep working (no deadlock, no write to a dead channel).
func TestWorkerPanicRestarts(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, &panicDriver{inner: NewSQLiteDriver()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()
	m.SetAsyncConfig(AsyncConfig{Enabled: true, BatchSize: 64, FlushInterval: 20 * time.Millisecond, ChannelSize: 16})

	if err := m.Insert(&model.Log{Model: "pre-panic"}); err != nil {
		t.Fatalf("pre-panic insert: %v", err)
	}
	// Panic the worker: the inner driver panics on the first insert.
	if err := m.Insert(&model.Log{Model: "panic-now"}); err != nil {
		t.Fatalf("panic-trigger insert: %v", err)
	}
	// The recovery sleeps 100ms before restarting; give it time.
	deadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.RLock()
		alive := m.workerAlive
		m.mu.RUnlock()
		if alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker never restarted after panic")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Post-restart inserts must flow through the fresh channel.
	for i := 0; i < 32; i++ {
		if err := m.Insert(&model.Log{Model: "post-restart"}); err != nil {
			t.Fatalf("post-restart insert %d: %v", i, err)
		}
	}
	m.Flush()
	rows, _, err := m.Query(QueryFilter{}, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// The panic-trigger insert was also recovered via synchronous
	// fallback during the dead window.
	if len(rows) < 33 {
		t.Fatalf("rows = %d, want >= 33", len(rows))
	}
}

// panicDriver panics inside Insert to simulate a corrupt batch.
type panicDriver struct {
	inner Driver
}

func (p *panicDriver) Open(dir string) error { return p.inner.Open(dir) }
func (p *panicDriver) Close() error          { return p.inner.Close() }
func (p *panicDriver) Insert(entry *model.Log) error {
	if entry.Model == "panic-now" {
		panic("simulated batch insert panic")
	}
	return p.inner.Insert(entry)
}
func (p *panicDriver) BatchInsert(entries []*model.Log) (int, error) {
	return p.inner.BatchInsert(entries)
}
func (p *panicDriver) TimeSeries(f QueryFilter, bucketSec int64, days []string) ([]SeriesBucket, error) {
	return p.inner.TimeSeries(f, bucketSec, days)
}
func (p *panicDriver) TopByField(f QueryFilter, field string, limit int, days []string) ([]NamedMetric, error) {
	return p.inner.TopByField(f, field, limit, days)
}
func (p *panicDriver) ListFiles() ([]string, error)    { return p.inner.ListFiles() }
func (p *panicDriver) DeleteFiles(days []string) error { return p.inner.DeleteFiles(days) }
func (p *panicDriver) LogStats(days []string) (LogStatsResult, error) {
	return p.inner.LogStats(days)
}
func (p *panicDriver) QueryAcross(filter QueryFilter, days []string) ([]model.Log, int64, error) {
	return p.inner.QueryAcross(filter, days)
}
