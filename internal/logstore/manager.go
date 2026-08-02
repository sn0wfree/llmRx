package logstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/model"
)

// AsyncConfig controls the async batch log writer. The default
// config (DefaultAsyncConfig) is used when New creates a Manager.
// Call SetAsyncConfig before the first Insert to override.
type AsyncConfig struct {
	Enabled       bool
	BatchSize     int
	FlushInterval time.Duration
	ChannelSize   int
	// DropOnFull controls what happens when the queue is full:
	//   true  (default) — the entry is dropped and counted. The
	//         request path never touches SQLite, so a saturated
	//         logstore degrades gracefully (lost audit rows under
	//         sustained overload) instead of stalling the gateway.
	//   false — synchronous insert fallback. Keeps every row but
	//         blocks the request on the log write; the sync insert
	//         contends with the batch worker for the SQLite write
	//         lock, which is what turns overload into a stall.
	DropOnFull bool
}

// DefaultAsyncConfig is the default async configuration.
//  - Enabled:       true (async on by default)
//  - BatchSize:     500 (flush when batch reaches this size)
//  - FlushInterval: 100ms (flush on timer even if batch is below size)
//  - ChannelSize:   2000 (backpressure buffer, ~2s at 1000 req/s)
//  - DropOnFull:    true (drop + count instead of blocking requests)
var DefaultAsyncConfig = AsyncConfig{
	Enabled:       true,
	BatchSize:     500,
	FlushInterval: 100 * time.Millisecond,
	ChannelSize:   2000,
	DropOnFull:    true,
}

// Manager is the package-level façade over a Driver. The store
// package and the admin handlers talk to Manager, not to Driver
// directly, so we have one place to log lifecycle events and to
// run the retention sweeper.
type Manager struct {
	driver Driver
	dir    string

	mu        sync.RWMutex
	started   bool
	closeOnce sync.Once

	// dropped counts log entries discarded because the queue was
	// full (DropOnFull mode). Atomic; exposed via Dropped().
	dropped int64

	// Async batch fields.
	asyncCfg    AsyncConfig
	ch          chan *model.Log
	flushCh     chan chan struct{}
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	startWorker sync.Once
	// workerAlive tracks whether a batch goroutine is currently
	// draining ch/flushCh. A panic restarts the worker (new
	// channels); Insert/Flush snapshot the pointers under mu so
	// they never touch a half-rebuilt worker.
	workerAlive bool
}

// New constructs a Manager rooted at dir using the provided
// driver. If driver is nil, NewSQLiteDriver is used. The async
// batch worker is started automatically (see DefaultAsyncConfig).
func New(dir string, driver Driver) (*Manager, error) {
	if dir == "" {
		return nil, errors.New("logstore: empty dir")
	}
	if driver == nil {
		driver = NewSQLiteDriver()
	}
	if err := driver.Open(dir); err != nil {
		return nil, err
	}
	m := &Manager{driver: driver, dir: dir, asyncCfg: DefaultAsyncConfig}
	return m, nil
}

// SetAsyncConfig overrides the async configuration. Must be called
// before any Insert. When called with Enabled=false, Insert reverts
// to synchronous.
func (m *Manager) SetAsyncConfig(cfg AsyncConfig) {
	m.asyncCfg = cfg
}

// Flush forces the async worker to flush any pending entries. This
// is a no-op when async is disabled or the worker has not been
// started. Useful for tests and graceful shutdown.
func (m *Manager) Flush() {
	m.mu.RLock()
	ch, flushCh, alive := m.ch, m.flushCh, m.workerAlive
	m.mu.RUnlock()
	if ch == nil || flushCh == nil || !alive {
		return
	}
	done := make(chan struct{})
	flushCh <- done
	<-done
}

// Dir returns the storage directory. Useful for diagnostics.
func (m *Manager) Dir() string { return m.dir }

// Dropped returns how many log entries were discarded because the
// async queue was full (DropOnFull mode). Zero in strict mode.
func (m *Manager) Dropped() int64 { return atomic.LoadInt64(&m.dropped) }

// Insert writes a single log entry. When async is enabled the entry
// is enqueued to the batch worker. If the channel is full the
// behavior depends on DropOnFull: default is to drop the entry and
// count it (the request path never blocks on SQLite); strict mode
// (DropOnFull=false) falls back to a synchronous insert so no row
// is ever lost, at the cost of blocking the caller under overload.
func (m *Manager) Insert(entry *model.Log) error {
	if !m.asyncCfg.Enabled {
		return m.driver.Insert(entry)
	}
	m.startWorker.Do(func() {
		m.startBatchWorker()
	})
	m.mu.RLock()
	ch, alive := m.ch, m.workerAlive
	m.mu.RUnlock()
	if !alive {
		// Worker is (re)starting after a panic — insert
		// synchronously rather than enqueueing to a dead channel.
		return m.driver.Insert(entry)
	}
	select {
	case ch <- entry:
		return nil
	default:
		if !m.asyncCfg.DropOnFull {
			// Strict mode: keep every row, pay the sync cost.
			return m.driver.Insert(entry)
		}
		// Drop + count. Rate-limited warn: one log line per
		// thousand drops, so a saturated logstore stays audible
		// without spamming the error log.
		n := atomic.AddInt64(&m.dropped, 1)
		if n%1000 == 1 {
			logging.Warn("logstore queue full — dropping log entries",
				logging.F("dropped_total", n),
			)
		}
		return nil
	}
}

// BatchInsert inserts multiple log entries in a single transaction.
func (m *Manager) BatchInsert(entries []*model.Log) (int, error) {
	return m.driver.BatchInsert(entries)
}

// Query returns paginated rows across the given days. days=nil
// means "every file the driver knows about".
func (m *Manager) Query(filter QueryFilter, days []string) ([]model.Log, int64, error) {
	return m.driver.QueryAcross(filter, days)
}

// Stats aggregates token/cost/error totals across the given days.
func (m *Manager) Stats(days []string) (LogStatsResult, error) {
	return m.driver.LogStats(days)
}

// TimeSeries groups matching rows into bucketSec-second windows
// across the given days.
func (m *Manager) TimeSeries(filter QueryFilter, bucketSec int64, days []string) ([]SeriesBucket, error) {
	return m.driver.TimeSeries(filter, bucketSec, days)
}

// TopByField wraps Driver.TopByField at the manager level.
func (m *Manager) TopByField(filter QueryFilter, field string, limit int, days []string) ([]NamedMetric, error) {
	return m.driver.TopByField(filter, field, limit, days)
}

// ListFiles returns all current log file basenames.
func (m *Manager) ListFiles() ([]string, error) {
	return m.driver.ListFiles()
}

// DeleteFiles is exposed so admin tooling can prune beyond the
// retention window manually.
func (m *Manager) DeleteFiles(days []string) error {
	return m.driver.DeleteFiles(days)
}

// Close flushes any pending entries and releases driver resources.
func (m *Manager) Close() error {
	var err error
	m.closeOnce.Do(func() {
		m.mu.RLock()
		ch, cancel := m.ch, m.cancel
		m.mu.RUnlock()
		if ch != nil && cancel != nil {
			cancel()
			m.wg.Wait()
		}
		err = m.driver.Close()
	})
	return err
}

// startBatchWorker launches the async batch goroutine (once via
// sync.Once, again after a panic restart). Safe to call even when
// async is disabled (creates a channel that will never receive
// entries, but Close will drain it correctly). Rebuilds the
// channels under mu so concurrent Insert/Flush see a consistent
// snapshot.
func (m *Manager) startBatchWorker() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ch = make(chan *model.Log, m.asyncCfg.ChannelSize)
	m.flushCh = make(chan chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.workerAlive = true
	m.wg.Add(1)
	go m.runBatchLoop(ctx)
}

// runBatchLoop is the inner loop of the batch worker. It drains
// entries from the channel, flushes on batch size or timer, and
// exits when ctx is cancelled. Panic recovery restarts the loop.
func (m *Manager) runBatchLoop(ctx context.Context) {
	defer m.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			logging.Error("logstore worker panic",
				logging.F("recover", r),
			)
			// Mark dead first so concurrent Insert/Flush don't
			// send into channels nobody drains, then restart
			// (startBatchWorker rebuilds the channels under mu).
			m.mu.Lock()
			m.workerAlive = false
			m.mu.Unlock()
			time.Sleep(100 * time.Millisecond)
			m.startBatchWorker()
		}
	}()
	ticker := time.NewTicker(m.asyncCfg.FlushInterval)
	defer ticker.Stop()
	batch := make([]*model.Log, 0, m.asyncCfg.BatchSize)
	for {
		select {
		case <-ctx.Done():
			// Drain the channel before flushing.
			for {
				select {
				case entry := <-m.ch:
					batch = append(batch, entry)
					if len(batch) >= m.asyncCfg.BatchSize {
						m.flush(batch)
						batch = make([]*model.Log, 0, m.asyncCfg.BatchSize)
					}
				default:
					if len(batch) > 0 {
						m.flush(batch)
					}
					return
				}
			}
		case entry := <-m.ch:
			batch = append(batch, entry)
			if len(batch) >= m.asyncCfg.BatchSize {
				m.flush(batch)
				batch = make([]*model.Log, 0, m.asyncCfg.BatchSize)
			}
		case done := <-m.flushCh:
			// Drain the channel completely before flushing.
		drainLoop:
			for {
				select {
				case entry := <-m.ch:
					batch = append(batch, entry)
					if len(batch) >= m.asyncCfg.BatchSize {
						m.flush(batch)
						batch = make([]*model.Log, 0, m.asyncCfg.BatchSize)
					}
				default:
					break drainLoop
				}
			}
			if len(batch) > 0 {
				m.flush(batch)
				batch = make([]*model.Log, 0, m.asyncCfg.BatchSize)
			}
			close(done)
		case <-ticker.C:
			if len(batch) > 0 {
				m.flush(batch)
				batch = make([]*model.Log, 0, m.asyncCfg.BatchSize)
			}
		}
	}
}

// flush sends the accumulated batch to the driver. Errors are
// logged but not returned (the worker has no caller to propagate
// to). Empty batches are a no-op.
func (m *Manager) flush(batch []*model.Log) {
	if len(batch) == 0 {
		return
	}
	if _, err := m.driver.BatchInsert(batch); err != nil {
		logging.Warn("logstore batch insert failed",
			logging.F("count", len(batch)),
			logging.F("error", err.Error()),
		)
	}
}

// RunRetention periodically deletes log files older than the
// number of days reported by retentionDays() on each tick. The
// caller passes a function so admin updates to the retention
// window take effect on the next sweep instead of being frozen
// at goroutine start.
//
// retentionDays() <= 0 disables the sweep entirely. The sweep
// runs once on entry (so admin changes don't have to wait 24h)
// and then every 24h. Exits when ctx is cancelled.
func (m *Manager) RunRetention(ctx context.Context, retentionDays func() int) {
	cur := retentionDays()
	if cur <= 0 {
		logging.Info("logstore retention disabled (retention_days <= 0)")
		return
	}

	sweep := func() {
		days := retentionDays()
		if days <= 0 {
			return
		}
		cutoff := time.Now().UTC().AddDate(0, 0, -days)
		cutoffDay := cutoff.Format("2006-01-02")

		files, err := m.driver.ListFiles()
		if err != nil {
			logging.Warn("logstore list files failed", logging.F("error", err.Error()))
			return
		}

		var toDelete []string
		for _, f := range files {
			date := extractDate(f)
			if date < cutoffDay {
				toDelete = append(toDelete, f)
			}
		}
		if len(toDelete) == 0 {
			return
		}
		if err := m.driver.DeleteFiles(toDelete); err != nil {
			logging.Warn("logstore retention delete failed", logging.F("error", err.Error()))
			return
		}
		logging.Info("logstore retention deleted files",
			logging.F("files", len(toDelete)),
			logging.F("days", days),
		)
	}

	sweep()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}

// EnsureDir is a small helper for callers that want to pre-create
// the log directory (e.g. main.go). It's idempotent.
func EnsureDir(dir string) error {
	if dir == "" {
		return errors.New("logstore: empty dir")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("logstore: mkdir %s: %w", dir, err)
	}
	return nil
}

// SanitizeDay strips a path prefix and .db suffix from a filename,
// producing the canonical day key ("YYYY-MM-DD" or "YYYY-MM-DD-N").
// Used by tests and admin tooling that want to display file
// identifiers.
func SanitizeDay(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, ".db")
}
