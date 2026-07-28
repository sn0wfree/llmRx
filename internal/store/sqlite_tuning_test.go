package store

import (
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
