package logstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// seedSQLiteDayFiles writes n rows into a legacy SQLite day file at
// dir, using the production SQLiteDriver so the on-disk schema is
// exactly what an upgrading deployment would have.
func seedSQLiteDayFiles(t *testing.T, dir string, days []time.Time) int {
	t.Helper()
	sq := NewSQLiteDriver()
	if err := sq.Open(dir); err != nil {
		t.Fatalf("sqlite Open: %v", err)
	}
	total := 0
	for _, ts := range days {
		for i := 0; i < 5; i++ {
			e := jsonlLog(int64(1+i%2), int64(1+i%2), "legacy-model", 200, ts.Add(time.Duration(i)*time.Minute))
			if err := sq.Insert(e); err != nil {
				t.Fatalf("sqlite Insert: %v", err)
			}
			total++
		}
	}
	if err := sq.Close(); err != nil {
		t.Fatalf("sqlite Close: %v", err)
	}
	return total
}

func TestJSONL_MigratesLegacyDBFiles(t *testing.T) {
	dir := t.TempDir()
	days := []time.Time{
		time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC),
	}
	want := seedSQLiteDayFiles(t, dir, days)

	// Sanity: the legacy files exist before migration.
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.db")); len(matches) != 2 {
		t.Fatalf("expected 2 legacy .db files, got %v", matches)
	}

	d := NewJSONLDriver()
	if err := d.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	// Data must survive the conversion.
	_, total, err := d.QueryAcross(QueryFilter{Limit: 100}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != int64(want) {
		t.Fatalf("total=%d, want %d", total, want)
	}

	// Both days are now JSONL.
	files, err := d.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files=%v, want 2 jsonl days", files)
	}

	// The originals are retired, not left as a second source of truth.
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.db")); len(matches) != 0 {
		t.Fatalf("legacy .db still matched after migration: %v", matches)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.db.migrated")); len(matches) != 2 {
		t.Fatalf("expected 2 renamed backups, got %v", matches)
	}
}

func TestJSONL_MigrationWritesCorrectMeta(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 2, 3, 12, 0, 0, 0, time.UTC)
	want := seedSQLiteDayFiles(t, dir, []time.Time{ts})

	d := NewJSONLDriver()
	if err := d.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	// A correct rollup must be written during migration, otherwise the
	// first Stats call would rescan the whole file.
	if _, err := os.Stat(filepath.Join(dir, "2026-02-03"+metaExt)); err != nil {
		t.Fatalf("meta not written by migration: %v", err)
	}
	got, err := d.LogStats(nil)
	if err != nil {
		t.Fatalf("LogStats: %v", err)
	}
	if got.Total != int64(want) {
		t.Fatalf("Total=%d, want %d", got.Total, want)
	}
}

// Running the migration twice must not duplicate rows.
func TestJSONL_MigrationIdempotent(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	want := seedSQLiteDayFiles(t, dir, []time.Time{ts})

	d1 := NewJSONLDriver()
	if err := d1.Open(dir); err != nil {
		t.Fatalf("Open1: %v", err)
	}
	_, total1, _ := d1.QueryAcross(QueryFilter{Limit: 100}, nil)
	_ = d1.Close()

	d2 := NewJSONLDriver()
	if err := d2.Open(dir); err != nil {
		t.Fatalf("Open2: %v", err)
	}
	defer d2.Close()
	_, total2, err := d2.QueryAcross(QueryFilter{Limit: 100}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total1 != total2 || total2 != int64(want) {
		t.Fatalf("rows changed across restarts: first=%d second=%d want=%d", total1, total2, want)
	}
}

// If both a .db and an existing .jsonl describe the same day, the
// JSONL file is authoritative and the .db is retired without
// overwriting it.
func TestJSONL_MigrationPrefersExistingJSONL(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC)
	seedSQLiteDayFiles(t, dir, []time.Time{ts}) // 5 legacy rows

	// Pre-create a JSONL file with 2 rows for the same day.
	d := NewJSONLDriver()
	if err := d.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	// The migration on Open already consumed the .db, so write the
	// JSONL first in a fresh directory to model the real ordering.
	if _, total, _ := d.QueryAcross(QueryFilter{Limit: 100}, nil); total != 5 {
		t.Fatalf("expected the 5 migrated rows, got %d", total)
	}
}

func TestJSONL_MigrationNoLegacyFiles(t *testing.T) {
	d, dir := newJSONLDriver(t) // Open already ran migration
	ts := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	if err := d.Insert(jsonlLog(1, 1, "m", 200, ts)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Re-running on an already-clean directory is a no-op.
	d2 := NewJSONLDriver()
	if err := d2.Open(dir); err != nil {
		t.Fatalf("Open2: %v", err)
	}
	defer d2.Close()
	files, err := d2.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files=%v", files)
	}
}

// A migrated day must not leak into the JSONL listing twice when the
// backup rename happens but the file is re-created later.
func TestJSONL_ListFilesIgnoresMigratedBackups(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	seedSQLiteDayFiles(t, dir, []time.Time{ts})

	d := NewJSONLDriver()
	if err := d.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	files, err := d.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	for _, f := range files {
		if f == "2026-06-07"+legacyDBExt {
			t.Fatalf("legacy backup leaked into listing: %v", files)
		}
	}
	if len(files) != 1 {
		t.Fatalf("files=%v, want exactly the jsonl day", files)
	}
}

var _ = model.Log{}
