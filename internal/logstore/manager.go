package logstore

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

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
}

// New constructs a Manager rooted at dir using the provided
// driver. If driver is nil, NewSQLiteDriver is used.
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
	return &Manager{driver: driver, dir: dir}, nil
}

// Dir returns the storage directory. Useful for diagnostics.
func (m *Manager) Dir() string { return m.dir }

// Insert writes a single log entry.
func (m *Manager) Insert(entry *model.Log) error {
	return m.driver.Insert(entry)
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

// Close releases driver resources.
func (m *Manager) Close() error {
	var err error
	m.closeOnce.Do(func() { err = m.driver.Close() })
	return err
}

// RunRetention periodically deletes log files older than
// retentionDays. retentionDays <= 0 disables the sweep entirely.
// The sweep runs once on entry (so admin changes don't have to
// wait 24h) and then every 24h. Exits when ctx is cancelled.
func (m *Manager) RunRetention(ctx context.Context, retentionDays int) {
	if retentionDays <= 0 {
		log.Printf("logstore: retention disabled (retention_days <= 0)")
		return
	}

	sweep := func() {
		cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
		cutoffDay := cutoff.Format("2006-01-02")

		files, err := m.driver.ListFiles()
		if err != nil {
			log.Printf("logstore: list files: %v", err)
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
			log.Printf("logstore: retention delete: %v", err)
			return
		}
		log.Printf("logstore: retention deleted %d files older than %d days",
			len(toDelete), retentionDays)
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
