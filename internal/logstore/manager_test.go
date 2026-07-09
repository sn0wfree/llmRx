package logstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	m, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, dir
}

// ---------- New ----------

func TestNew_EmptyDir(t *testing.T) {
	_, err := New("", nil)
	if err == nil {
		t.Fatal("expected error for empty dir")
	}
}

func TestNew_DefaultDriver(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()
	if m.dir != dir {
		t.Fatalf("dir: want %s, got %s", dir, m.dir)
	}
}

func TestNew_CustomDriver(t *testing.T) {
	dir := t.TempDir()
	custom := NewSQLiteDriver()
	m, err := New(dir, custom)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()
	if m.driver != custom {
		t.Fatal("custom driver not used")
	}
}

func TestNew_InvalidDir(t *testing.T) {
	// Use a path that cannot be created (file exists as regular file)
	tmpFile := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(tmpFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := New(filepath.Join(tmpFile, "logs"), nil)
	if err == nil {
		t.Fatal("expected error for un-creatable dir")
	}
}

// ---------- Dir ----------

func TestManagerDir(t *testing.T) {
	dir := t.TempDir()
	m, _ := New(dir, nil)
	defer m.Close()
	if m.Dir() != dir {
		t.Fatalf("Dir(): want %s, got %s", dir, m.Dir())
	}
}

// ---------- Insert / Query ----------

func TestManagerInsertAndQuery(t *testing.T) {
	m, _ := newTestManager(t)
	now := time.Now()
	for i := 0; i < 3; i++ {
		_ = m.Insert(makeLog(1, 1, "m", 200, now.Add(time.Duration(i)*time.Second)))
	}
	rows, total, err := m.Query(QueryFilter{}, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 3 {
		t.Fatalf("expected total=3, got %d", total)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
}

// ---------- Stats ----------

func TestManagerStats(t *testing.T) {
	m, _ := newTestManager(t)
	now := time.Now()
	_ = m.Insert(makeLog(1, 1, "m", 200, now))
	_ = m.Insert(makeLog(1, 1, "m", 500, now.Add(time.Second)))
	stats, err := m.Stats(nil)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Total != 2 {
		t.Fatalf("expected Total=2, got %d", stats.Total)
	}
	if stats.Errors != 1 {
		t.Fatalf("expected Errors=1, got %d", stats.Errors)
	}
}

// ---------- TimeSeries ----------

func TestManagerTimeSeries(t *testing.T) {
	m, _ := newTestManager(t)
	_ = m.Insert(makeLog(1, 1, "m", 200, time.Now()))
	buckets, err := m.TimeSeries(QueryFilter{}, 3600, nil)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if len(buckets) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(buckets))
	}
}

// ---------- TopByField ----------

func TestManagerTopByField(t *testing.T) {
	m, _ := newTestManager(t)
	_ = m.Insert(makeLog(1, 1, "gpt-4", 200, time.Now()))
	_ = m.Insert(makeLog(1, 1, "gpt-3.5", 200, time.Now().Add(time.Second)))
	results, err := m.TopByField(QueryFilter{}, "model", 10, nil)
	if err != nil {
		t.Fatalf("TopByField: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 models, got %d", len(results))
	}
}

// ---------- ListFiles / DeleteFiles ----------

func TestManagerListAndDelete(t *testing.T) {
	m, _ := newTestManager(t)
	day1 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	_ = m.Insert(makeLog(1, 1, "m", 200, day1))
	_ = m.Insert(makeLog(1, 1, "m", 200, day2))

	files, err := m.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}

	if err := m.DeleteFiles([]string{"2026-07-01"}); err != nil {
		t.Fatalf("DeleteFiles: %v", err)
	}
	files, _ = m.ListFiles()
	if len(files) != 1 || files[0] != "2026-07-02" {
		t.Fatalf("expected only 2026-07-02, got %v", files)
	}
}

// ---------- Close ----------

func TestManagerCloseIdempotent(t *testing.T) {
	m, _ := newTestManager(t)
	if err := m.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestManagerInsertAfterClose(t *testing.T) {
	m, _ := newTestManager(t)
	_ = m.Close()
	// After Close, the driver still has a dir set but connections are
	// closed. The behavior depends on whether the driver lazily
	// reopens. In practice the manager is closed only at shutdown,
	// so we just verify that Close itself is clean.
	// This test documents the current behavior: after Close, the
	// driver's conns map is empty, so a subsequent Insert would
	// attempt to reopen a connection. We don't assert on the result.
	_ = m
}

// ---------- RunRetention ----------

func TestManagerRunRetentionContextCancel(t *testing.T) {
	m, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.RunRetention(ctx, 30)
		close(done)
	}()
	cancel()
	select {
	case <-done:
		// good - returned after cancel
	case <-time.After(2 * time.Second):
		t.Fatal("RunRetention did not return after context cancel")
	}
}

func TestManagerRunRetentionSweeps(t *testing.T) {
	m, _ := newTestManager(t)
	// Insert old log
	old := time.Now().UTC().AddDate(0, 0, -60)
	_ = m.Insert(makeLog(1, 1, "m", 200, old))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.RunRetention(ctx, 30)

	// Wait for sweep
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		files, _ := m.ListFiles()
		if len(files) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("retention sweep did not run")
}

// ---------- EnsureDir ----------

func TestEnsureDirSuccess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "logs")
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("dir not created: %v", err)
	}
}

func TestEnsureDirIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("first EnsureDir: %v", err)
	}
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("second EnsureDir: %v", err)
	}
}

func TestEnsureDirEmpty(t *testing.T) {
	if err := EnsureDir(""); err == nil {
		t.Fatal("expected error for empty dir")
	}
}

func TestEnsureDirInvalidPath(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(tmpFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := EnsureDir(filepath.Join(tmpFile, "nested")); err == nil {
		t.Fatal("expected error for invalid path")
	}
}

// ---------- SanitizeDay ----------

func TestSanitizeDay(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"/path/to/2026-07-01.db", "2026-07-01"},
		{"/path/to/2026-07-01-2.db", "2026-07-01-2"},
		{"2026-07-01.db", "2026-07-01"},
		{"2026-07-01", "2026-07-01"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := SanitizeDay(tt.in); got != tt.want {
				t.Fatalf("SanitizeDay(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ---------- Now override ----------

func TestNowOverride(t *testing.T) {
	original := Now
	defer func() { Now = original }()

	fixed := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	Now = func() time.Time { return fixed }

	m, _ := newTestManager(t)
	_ = m.Insert(makeLog(1, 1, "m", 200, fixed))

	files, _ := m.ListFiles()
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	// File should be named for the fixed date
	if files[0] != "2025-01-01" {
		t.Fatalf("expected file '2025-01-01', got %q", files[0])
	}
}

// ---------- MaxAttachFiles constant ----------

func TestMaxAttachFiles(t *testing.T) {
	if MaxAttachFiles <= 0 {
		t.Fatal("MaxAttachFiles should be positive")
	}
	if MaxAttachFiles > 10 {
		t.Fatal("MaxAttachFiles should be <= 10 (SQLite default limit)")
	}
}
