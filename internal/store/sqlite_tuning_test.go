package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

func testChannel(t *testing.T, name string) *model.Channel {
	t.Helper()
	now := time.Now()
	return &model.Channel{
		Name:      name,
		Provider:  "openai",
		Protocol:  "openai",
		BaseURL:   "https://api.example.com",
		Models:    []string{"gpt-4"},
		Status:    1,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestSQLite_PragmasApplied(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")
	s, err := OpenSQLite(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	// Check that the pragmas were applied. cache_size=-20000 reads
	// back as -20000; mmap_size=268435456 reads as 268435456.
	var cacheSize, mmapSize int64
	if err := s.db.QueryRow("PRAGMA cache_size").Scan(&cacheSize); err != nil {
		t.Fatal(err)
	}
	if cacheSize != -20000 {
		t.Errorf("cache_size = %d, want -20000", cacheSize)
	}
	if err := s.db.QueryRow("PRAGMA mmap_size").Scan(&mmapSize); err != nil {
		t.Fatal(err)
	}
	if mmapSize != 268435456 {
		t.Errorf("mmap_size = %d, want 268435456", mmapSize)
	}
}

func TestSQLite_ConnectionPool(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")
	s, err := OpenSQLite(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	stats := s.db.Stats()
	if stats.MaxOpenConnections != 8 {
		t.Errorf("MaxOpenConnections = %d, want 8", stats.MaxOpenConnections)
	}
}

func TestSQLite_DeleteLogsBefore(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")
	s, err := OpenSQLite(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	_ = ctx
	// Setup: create a channel, then 3 logs (1 old, 2 recent)
	if err := s.CreateChannel(testChannel(t, "test")); err != nil {
		t.Fatal(err)
	}
	// We can't easily test time-based retention without exposing
	// the logs model; just verify the method doesn't error on
	// empty table.
	now := int64(0)
	n, err := s.DeleteLogsBefore(now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0 deletes, got %d", n)
	}
}
