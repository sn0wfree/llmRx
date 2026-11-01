package logstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// Retention is the only mechanism that deletes user log data, and it
// is shared by every backend, so each driver needs its own end-to-end
// test proving the right files disappear and the right ones survive.

// seedDaysForRetention writes one row per day into drv, using dates
// well in the past so a short retention window expires them.
func seedDaysForRetention(t *testing.T, drv Driver, days ...string) {
	t.Helper()
	for _, day := range days {
		ts, err := time.Parse("2006-01-02", day)
		if err != nil {
			t.Fatalf("parse %s: %v", day, err)
		}
		if err := drv.Insert(jsonlLog(1, 1, "m", 200, ts.Add(12*time.Hour))); err != nil {
			t.Fatalf("Insert %s: %v", day, err)
		}
	}
}

// runRetentionOnce drives one sweep synchronously: RunRetention
// sweeps on entry, so cancelling the context immediately after start
// gives exactly one pass.
func runRetentionOnce(t *testing.T, m *Manager, days int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.RunRetention(ctx, func() int { return days })
	}()
	// Give the entry sweep a moment to finish, then stop the loop.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunRetention did not return after cancel")
	}
}

func newJSONLManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	m, err := New(dir, NewJSONLDriver())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, dir
}

func fileExists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func TestJSONL_RetentionDeletesExpiredDays(t *testing.T) {
	m, dir := newJSONLManager(t)
	expired := "2020-01-01"
	fresh := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	seedDaysForRetention(t, m.driver, expired, fresh)
	m.Flush()

	runRetentionOnce(t, m, 7)

	if fileExists(dir, expired+jsonlExt) {
		t.Fatalf("expired day file %s survived retention", expired)
	}
	if fileExists(dir, expired+metaExt) {
		t.Fatalf("expired meta %s survived retention", expired)
	}
	if !fileExists(dir, fresh+jsonlExt) {
		t.Fatalf("fresh day file %s was deleted", fresh)
	}

	// Nothing expired should still be queryable.
	rows, total, err := m.Query(QueryFilter{Limit: 100}, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("total=%d len=%d, want only the fresh day", total, len(rows))
	}
}

func TestJSONL_RetentionKeepsRecentDays(t *testing.T) {
	m, dir := newJSONLManager(t)
	today := time.Now().UTC().Format("2006-01-02")
	seedDaysForRetention(t, m.driver, today)
	m.Flush()

	runRetentionOnce(t, m, 30)

	if !fileExists(dir, today+jsonlExt) {
		t.Fatal("today's log file was deleted by a 30-day retention window")
	}
}

func TestJSONL_RetentionArchivesExpiredDays(t *testing.T) {
	m, dir := newJSONLManager(t)
	expired := "2020-02-02"
	seedDaysForRetention(t, m.driver, expired)
	m.Flush()
	m.SetRetentionMode(func() string { return "archive" })

	runRetentionOnce(t, m, 7)

	if fileExists(dir, expired+jsonlExt) {
		t.Fatal("uncompressed day file survived archive mode")
	}
	if !fileExists(dir, expired+jsonlExt+".gz") {
		t.Fatal("gzip archive was not created")
	}
	if fileExists(dir, expired+metaExt) {
		t.Fatal("meta rollup for an archived day was left behind")
	}

	// Archived days must drop out of listFiles so retention does not
	// try to expire them again forever.
	files, err := m.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("archived day still listed: %v", files)
	}
}

func TestJSONL_RetentionDisabled(t *testing.T) {
	m, dir := newJSONLManager(t)
	expired := "2020-03-03"
	seedDaysForRetention(t, m.driver, expired)
	m.Flush()

	runRetentionOnce(t, m, 0)

	if !fileExists(dir, expired+jsonlExt) {
		t.Fatal("retention with days<=0 deleted files")
	}
}

// Legacy .db backups produced by migration must not accumulate
// forever: once the day expires, its .db.migrated copy has to go too.
func TestJSONL_RetentionRemovesMigratedBackups(t *testing.T) {
	dir := t.TempDir()
	old := time.Date(2020, 4, 4, 12, 0, 0, 0, time.UTC)
	sq := NewSQLiteDriver()
	if err := sq.Open(dir); err != nil {
		t.Fatalf("sqlite Open: %v", err)
	}
	if err := sq.Insert(jsonlLog(1, 1, "m", 200, old)); err != nil {
		t.Fatalf("sqlite Insert: %v", err)
	}
	_ = sq.Close()

	// Open performs the migration, leaving 2020-04-04.db.migrated.
	m, err := New(dir, NewJSONLDriver())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()
	m.Flush()

	if !fileExists(dir, "2020-04-04.db.migrated") {
		t.Fatal("migration backup missing; test setup is wrong")
	}

	runRetentionOnce(t, m, 7)

	if fileExists(dir, "2020-04-04.jsonl") {
		t.Fatal("expired jsonl survived")
	}
	if fileExists(dir, "2020-04-04.db.migrated") {
		t.Fatal("migrated backup survived retention and will accumulate forever")
	}
}

// An archive-mode sweep must also retire the legacy backup, otherwise
// the compressed archive and the full-size .db copy both stay.
func TestJSONL_RetentionArchiveRemovesMigratedBackups(t *testing.T) {
	dir := t.TempDir()
	old := time.Date(2020, 5, 5, 12, 0, 0, 0, time.UTC)
	sq := NewSQLiteDriver()
	if err := sq.Open(dir); err != nil {
		t.Fatalf("sqlite Open: %v", err)
	}
	if err := sq.Insert(jsonlLog(1, 1, "m", 200, old)); err != nil {
		t.Fatalf("sqlite Insert: %v", err)
	}
	_ = sq.Close()

	m, err := New(dir, NewJSONLDriver())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()
	m.Flush()
	m.SetRetentionMode(func() string { return "archive" })

	runRetentionOnce(t, m, 7)

	if !fileExists(dir, "2020-05-05.jsonl.gz") {
		t.Fatal("archive not created")
	}
	if fileExists(dir, "2020-05-05.db.migrated") {
		t.Fatal("migrated backup survived archive-mode retention")
	}
}

// The SQLite backend's own retention behaviour is covered in
// sqlite_driver_test.go; this asserts the shared Manager sweep drives
// it correctly when selected explicitly, so future backend work cannot
// silently change the caller side.
func TestSQLite_RetentionDeletesExpiredDays(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, NewSQLiteDriver())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()

	seedDaysForRetention(t, m.driver, "2020-06-06")
	m.Flush()

	runRetentionOnce(t, m, 7)

	if fileExists(dir, "2020-06-06.db") {
		t.Fatal("sqlite expired day file survived retention")
	}
}

// A day whose data file is present but whose meta is missing must not
// break the retention sweep.
func TestJSONL_RetentionToleratesMissingMeta(t *testing.T) {
	m, dir := newJSONLManager(t)
	expired := "2020-07-07"
	seedDaysForRetention(t, m.driver, expired)
	m.Flush()

	if err := os.Remove(filepath.Join(dir, expired+metaExt)); err != nil {
		t.Fatalf("remove meta: %v", err)
	}

	runRetentionOnce(t, m, 7)

	if fileExists(dir, expired+jsonlExt) {
		t.Fatal("expired day survived when its meta was missing")
	}
}

// An orphaned backup — one whose .jsonl was already removed — is
// invisible to ListFiles, so the sweep must still find and expire it.
func TestJSONL_RetentionRemovesOrphanedMigratedBackup(t *testing.T) {
	dir := t.TempDir()
	orphan := filepath.Join(dir, "2020-08-08.db.migrated")
	if err := os.WriteFile(orphan, []byte("stale backup"), 0o644); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}

	m, err := New(dir, NewJSONLDriver())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()

	files, _ := m.ListFiles()
	if len(files) != 0 {
		t.Fatalf("setup wrong: orphan should not be listed, got %v", files)
	}

	runRetentionOnce(t, m, 7)

	if fileExists(dir, "2020-08-08.db.migrated") {
		t.Fatal("orphaned migrated backup survived retention")
	}
}

// Backups newer than the retention window must survive.
func TestJSONL_RetentionKeepsRecentMigratedBackup(t *testing.T) {
	dir := t.TempDir()
	recent := time.Now().UTC().Format("2006-01-02") + ".db.migrated"
	if err := os.WriteFile(filepath.Join(dir, recent), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	m, err := New(dir, NewJSONLDriver())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()

	runRetentionOnce(t, m, 30)

	if !fileExists(dir, recent) {
		t.Fatal("recent migrated backup was deleted")
	}
}

// ExpiredLegacyKeys must not report days at or after the cutoff.
func TestJSONL_ExpiredLegacyKeys(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"2020-01-01.db.migrated",
		"2020-02-02.db",
		"2030-01-01.db.migrated",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	d := NewJSONLDriver()
	if err := d.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	got := d.ExpiredLegacyKeys("2025-01-01")
	want := []string{"2020-01-01", "2020-02-02"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

var _ = model.Log{}
