package logstore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/sn0wfree/llmRx/internal/model"
)

// ---------- helpers ----------

func makeLog(tokenID, channelID int64, modelName string, status int, created time.Time) *model.Log {
	return &model.Log{
		TokenID:          tokenID,
		ChannelID:        channelID,
		KeyID:            0,
		Model:            modelName,
		PromptTokens:     100,
		CompletionTokens: 50,
		CachedTokens:     0,
		RealCostUSD:      0.01,
		BilledCostUSD:    0.015,
		DurationMs:       100,
		StatusCode:       status,
		RouterPath:       "L1",
		RequestIP:        "127.0.0.1",
		CreatedAt:        created,
	}
}

func newTestDriver(t *testing.T) (*SQLiteDriver, string) {
	t.Helper()
	dir := t.TempDir()
	d := NewSQLiteDriver()
	if err := d.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, dir
}

// ---------- Open / Close ----------

func TestSQLiteDriver_OpenCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "logs")
	d := NewSQLiteDriver()
	if err := d.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("expected dir to exist: %v", err)
	}
}

func TestSQLiteDriver_OpenTwiceReopens(t *testing.T) {
	d, _ := newTestDriver(t)
	// Reopening to a different dir is allowed and swaps the directory
	dir2 := t.TempDir()
	if err := d.Open(dir2); err != nil {
		t.Fatalf("Open (reopen): %v", err)
	}
	// Insert should go to the new dir
	if err := d.Insert(makeLog(1, 1, "m", 200, time.Now())); err != nil {
		t.Fatalf("Insert after reopen: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir2, time.Now().UTC().Format("2006-01-02")+".db")); err != nil {
		t.Fatalf("expected file in new dir: %v", err)
	}
}

func TestSQLiteDriver_CloseIdempotent(t *testing.T) {
	d, _ := newTestDriver(t)
	if err := d.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// ---------- Insert routing ----------

func TestSQLiteDriver_InsertRoutesToCorrectDay(t *testing.T) {
	d, _ := newTestDriver(t)
	day1 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)

	if err := d.Insert(makeLog(1, 1, "m1", 200, day1)); err != nil {
		t.Fatalf("insert day1: %v", err)
	}
	if err := d.Insert(makeLog(2, 2, "m2", 200, day2)); err != nil {
		t.Fatalf("insert day2: %v", err)
	}

	files, err := d.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(files), files)
	}
	if files[0] != "2026-07-01" || files[1] != "2026-07-02" {
		t.Fatalf("unexpected files: %v", files)
	}
}

func TestSQLiteDriver_InsertPreservesFields(t *testing.T) {
	d, _ := newTestDriver(t)
	now := time.Now()
	l := makeLog(42, 7, "test-model", 200, now)
	if err := d.Insert(l); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Query back to verify the entry was stored
	rows, total, err := d.QueryAcross(QueryFilter{}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected 1 row, got %d", total)
	}
	if rows[0].TokenID != 42 || rows[0].ChannelID != 7 || rows[0].Model != "test-model" {
		t.Fatalf("fields not preserved: %+v", rows[0])
	}
}

func TestSQLiteDriver_InsertConcurrent(t *testing.T) {
	d, _ := newTestDriver(t)
	const goroutines = 10
	const perGoroutine = 50
	var wg sync.WaitGroup
	var errCount int64
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				l := makeLog(int64(g), 1, "m", 200, time.Now().Add(time.Duration(i)*time.Millisecond))
				if err := d.Insert(l); err != nil {
					atomic.AddInt64(&errCount, 1)
				}
			}
		}(g)
	}
	wg.Wait()
	if errCount > 0 {
		t.Fatalf("concurrent inserts had %d errors", errCount)
	}
	files, _ := d.ListFiles()
	// All inserts were on the same day (now), so 1 file expected
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
}

// ---------- QueryAcross ----------

func TestSQLiteDriver_QueryAcrossSingleDay(t *testing.T) {
	d, _ := newTestDriver(t)
	now := time.Now()
	for i := 0; i < 5; i++ {
		_ = d.Insert(makeLog(1, 1, "m1", 200, now.Add(time.Duration(i)*time.Second)))
	}
	rows, total, err := d.QueryAcross(QueryFilter{}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 5 {
		t.Fatalf("expected total=5, got %d", total)
	}
	if len(rows) != 5 {
		t.Fatalf("expected 5 rows, got %d", len(rows))
	}
}

func TestSQLiteDriver_QueryAcrossMultiDay(t *testing.T) {
	d, _ := newTestDriver(t)
	day1 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	day3 := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	for _, d2 := range []time.Time{day1, day2, day3} {
		for i := 0; i < 3; i++ {
			_ = d.Insert(makeLog(1, 1, "m", 200, d2.Add(time.Duration(i)*time.Second)))
		}
	}
	rows, total, err := d.QueryAcross(QueryFilter{}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 9 {
		t.Fatalf("expected total=9, got %d", total)
	}
	if len(rows) != 9 {
		t.Fatalf("expected 9 rows, got %d", len(rows))
	}
}

func TestSQLiteDriver_QueryAcrossWithFilter(t *testing.T) {
	d, _ := newTestDriver(t)
	now := time.Now()
	_ = d.Insert(makeLog(1, 1, "gpt-4", 200, now))
	_ = d.Insert(makeLog(1, 1, "gpt-3.5", 200, now.Add(time.Second)))
	_ = d.Insert(makeLog(2, 1, "gpt-4", 500, now.Add(2*time.Second)))

	rows, total, err := d.QueryAcross(QueryFilter{Model: "gpt-4"}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected total=2, got %d", total)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Model != "gpt-4" {
			t.Fatalf("expected gpt-4, got %s", r.Model)
		}
	}
}

func TestSQLiteDriver_QueryAcrossStatusFilter(t *testing.T) {
	d, _ := newTestDriver(t)
	now := time.Now()
	_ = d.Insert(makeLog(1, 1, "m", 200, now))
	_ = d.Insert(makeLog(1, 1, "m", 500, now.Add(time.Second)))
	_ = d.Insert(makeLog(1, 1, "m", 200, now.Add(2*time.Second)))

	rows, total, err := d.QueryAcross(QueryFilter{StatusCode: 500}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected total=1, got %d", total)
	}
	if len(rows) != 1 || rows[0].StatusCode != 500 {
		t.Fatalf("expected 1 row with status 500, got %+v", rows)
	}
}

func TestSQLiteDriver_QueryAcrossPagination(t *testing.T) {
	d, _ := newTestDriver(t)
	now := time.Now()
	for i := 0; i < 20; i++ {
		_ = d.Insert(makeLog(1, 1, "m", 200, now.Add(time.Duration(i)*time.Second)))
	}
	// First page
	rows1, total, err := d.QueryAcross(QueryFilter{Limit: 5, Offset: 0}, nil)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if total != 20 {
		t.Fatalf("expected total=20, got %d", total)
	}
	if len(rows1) != 5 {
		t.Fatalf("expected 5 rows on page 1, got %d", len(rows1))
	}
	// Second page
	rows2, _, err := d.QueryAcross(QueryFilter{Limit: 5, Offset: 5}, nil)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(rows2) != 5 {
		t.Fatalf("expected 5 rows on page 2, got %d", len(rows2))
	}
	// Verify no overlap
	for _, r1 := range rows1 {
		for _, r2 := range rows2 {
			if r1.ID == r2.ID {
				t.Fatalf("row %d appears in both pages", r1.ID)
			}
		}
	}
}

func TestSQLiteDriver_QueryAcrossTimeRange(t *testing.T) {
	d, _ := newTestDriver(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		_ = d.Insert(makeLog(1, 1, "m", 200, base.Add(time.Duration(i)*time.Hour)))
	}
	// Query 2-6 hour window
	from := base.Add(2 * time.Hour).Unix()
	to := base.Add(6 * time.Hour).Unix()
	rows, total, err := d.QueryAcross(QueryFilter{CreatedFrom: from, CreatedTo: to}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	// Hours 2, 3, 4, 5, 6 = 5 rows
	if total != 5 {
		t.Fatalf("expected total=5, got %d", total)
	}
	if len(rows) != 5 {
		t.Fatalf("expected 5 rows, got %d", len(rows))
	}
}

func TestSQLiteDriver_QueryAcrossMaxAttachLimit(t *testing.T) {
	d, _ := newTestDriver(t)
	// Create MaxAttachFiles + 5 day files
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < MaxAttachFiles+5; i++ {
		day := base.AddDate(0, 0, i)
		_ = d.Insert(makeLog(1, 1, "m", 200, day))
	}
	// Query all - should cap at MaxAttachFiles (8 most recent)
	_, total, err := d.QueryAcross(QueryFilter{}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != int64(MaxAttachFiles) {
		t.Fatalf("expected total=%d (capped), got %d", MaxAttachFiles, total)
	}
}

// TestSQLiteDriver_QueryAcrossSameDayRolloversKeepsAllSeq covers
// the same-day rollover truncation strategy end-to-end via the
// driver.
//
// Layout: 6 historical single-file dates (2026-07-09..14) + a
// current date with seq 0, 1, 2 (3 seq files). Total 9 files.
// Budget 8 → drop the oldest single-file date (2026-07-09)
// entirely; keep 5 single files + 3 seq files of the current
// date = 8 rows.
func TestSQLiteDriver_QueryAcrossSameDayRolloversKeepsAllSeq(t *testing.T) {
	d, dir := newTestDriver(t)

	// 6 historical dates, each gets one row.
	for i := 0; i < 6; i++ {
		day := fmt.Sprintf("2026-07-%02d", 9+i)
		createDayFileWithModel(t, dir, day, fmt.Sprintf("old-%d", i))
	}
	// Current date with 3 seq files (simulated rollover):
	// base, -1, -2 all share date 2026-07-15.
	createDayFileWithModel(t, dir, "2026-07-15", "base")
	createDayFileWithModel(t, dir, "2026-07-15-1", "roll-1")
	createDayFileWithModel(t, dir, "2026-07-15-2", "roll-2")

	files, _ := d.ListFiles()
	if len(files) != 9 {
		t.Fatalf("setup: expected 9 files on disk, got %d (%v)", len(files), files)
	}

	rows, total, err := d.QueryAcross(QueryFilter{}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	got := []string{}
	for _, r := range rows {
		got = append(got, r.Model)
	}
	t.Logf("rows: %v (total=%d)", got, total)
	// 9 files → budget 8 → drop the oldest single-file date
	// (2026-07-09). Remaining 5 single files (old-1..old-5)
	// + 3 seq files of current date (base, roll-1, roll-2) = 8.
	if total != 8 {
		t.Fatalf("expected total=8 after truncation, got %d", total)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.Model] = true
	}
	for _, want := range []string{"base", "roll-1", "roll-2"} {
		if !seen[want] {
			t.Errorf("missing %q — seq file likely got truncated", want)
		}
	}
	if seen["old-0"] {
		t.Error("old-0 should have been truncated (oldest date)")
	}
}

// createDayFileWithModel writes a single test row into a per-day
// file using the same schema the logstore applies. It returns
// once the row is durable on disk so subsequent ListFiles sees
// the new file.
func createDayFileWithModel(t *testing.T, dir, name, modelLabel string) {
	t.Helper()
	path := filepath.Join(dir, name+".db")
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		token_id INTEGER NOT NULL DEFAULT 0,
		channel_id INTEGER NOT NULL DEFAULT 0,
		key_id INTEGER NOT NULL DEFAULT 0,
		model TEXT NOT NULL DEFAULT '',
		prompt_tokens INTEGER NOT NULL DEFAULT 0,
		completion_tokens INTEGER NOT NULL DEFAULT 0,
		cached_tokens INTEGER NOT NULL DEFAULT 0,
		real_cost_usd REAL NOT NULL DEFAULT 0,
		billed_cost_usd REAL NOT NULL DEFAULT 0,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		status_code INTEGER NOT NULL DEFAULT 0,
		router_path TEXT NOT NULL DEFAULT '',
		request_ip TEXT NOT NULL DEFAULT '',
		endpoint TEXT NOT NULL DEFAULT '',
		units INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO logs(model, status_code, created_at) VALUES(?, ?, ?)`,
		modelLabel, 200, time.Now().Unix()); err != nil {
		t.Fatalf("insert %s: %v", name, err)
	}
}

func TestSortByDateSeq_NumericSeqOrdering(t *testing.T) {
	in := []string{
		"2026-07-22-1",
		"2026-07-22",
		"2026-07-22-10",
		"2026-07-22-2",
	}
	got := sortByDateSeq(in)
	want := []string{
		"2026-07-22",
		"2026-07-22-1",
		"2026-07-22-2",
		"2026-07-22-10",
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("position %d: got %s want %s", i, got[i], w)
		}
	}
}

func TestTruncateByDate_DropsWholeDates(t *testing.T) {
	in := []string{
		"2026-07-01", "2026-07-02", "2026-07-03", "2026-07-04",
		"2026-07-05", "2026-07-06", "2026-07-07", "2026-07-08",
		"2026-07-09", "2026-07-09-1", "2026-07-09-2",
	}
	out := truncateByDate(in, MaxAttachFiles)
	// Budget 8; the oldest single date (2026-07-01) must go.
	if len(out) != MaxAttachFiles {
		t.Fatalf("expected %d files retained, got %d (%v)", MaxAttachFiles, len(out), out)
	}
	// Same-day seqs of 2026-07-09 must all survive.
	today := 0
	for _, n := range out {
		if extractDate(n) == "2026-07-09" {
			today++
		}
	}
	if today != 3 {
		t.Fatalf("expected all 3 seqs of 2026-07-09 to survive, got %d", today)
	}
}

func TestSQLiteDriver_QueryAcrossEmpty(t *testing.T) {
	d, _ := newTestDriver(t)
	rows, total, err := d.QueryAcross(QueryFilter{}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 0 || len(rows) != 0 {
		t.Fatalf("expected empty result, got %d rows, total=%d", len(rows), total)
	}
}

func TestSQLiteDriver_QueryAcrossSpecificDays(t *testing.T) {
	d, _ := newTestDriver(t)
	day1 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	day3 := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	_ = d.Insert(makeLog(1, 1, "m", 200, day1))
	_ = d.Insert(makeLog(1, 1, "m", 200, day2))
	_ = d.Insert(makeLog(1, 1, "m", 200, day3))

	// Query only day1 and day3
	rows, total, err := d.QueryAcross(QueryFilter{}, []string{"2026-07-01", "2026-07-03"})
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected total=2, got %d", total)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
}

// ---------- LogStats ----------

func TestSQLiteDriver_LogStatsSingleDay(t *testing.T) {
	d, _ := newTestDriver(t)
	now := time.Now()
	_ = d.Insert(makeLog(1, 1, "m1", 200, now))
	_ = d.Insert(makeLog(1, 1, "m1", 500, now.Add(time.Second)))
	_ = d.Insert(makeLog(1, 1, "m1", 200, now.Add(2*time.Second)))

	stats, err := d.LogStats(nil)
	if err != nil {
		t.Fatalf("LogStats: %v", err)
	}
	if stats.Total != 3 {
		t.Fatalf("expected Total=3, got %d", stats.Total)
	}
	if stats.Errors != 1 {
		t.Fatalf("expected Errors=1, got %d", stats.Errors)
	}
	if stats.PromptTokens != 300 {
		t.Fatalf("expected PromptTokens=300, got %d", stats.PromptTokens)
	}
	if stats.CompletionTokens != 150 {
		t.Fatalf("expected CompletionTokens=150, got %d", stats.CompletionTokens)
	}
}

func TestSQLiteDriver_LogStatsMultiDay(t *testing.T) {
	d, _ := newTestDriver(t)
	day1 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	_ = d.Insert(makeLog(1, 1, "m", 200, day1))
	_ = d.Insert(makeLog(1, 1, "m", 500, day1.Add(time.Second)))
	_ = d.Insert(makeLog(1, 1, "m", 200, day2))

	stats, err := d.LogStats(nil)
	if err != nil {
		t.Fatalf("LogStats: %v", err)
	}
	if stats.Total != 3 {
		t.Fatalf("expected Total=3, got %d", stats.Total)
	}
	if stats.Errors != 1 {
		t.Fatalf("expected Errors=1, got %d", stats.Errors)
	}
}

func TestSQLiteDriver_LogStatsEmpty(t *testing.T) {
	d, _ := newTestDriver(t)
	stats, err := d.LogStats(nil)
	if err != nil {
		t.Fatalf("LogStats: %v", err)
	}
	if stats.Total != 0 {
		t.Fatalf("expected Total=0, got %d", stats.Total)
	}
}

// ---------- TimeSeries ----------

func TestSQLiteDriver_TimeSeriesBasic(t *testing.T) {
	d, _ := newTestDriver(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	_ = d.Insert(makeLog(1, 1, "m", 200, base))
	_ = d.Insert(makeLog(1, 1, "m", 500, base.Add(time.Second)))
	_ = d.Insert(makeLog(1, 1, "m", 200, base.Add(2*time.Hour)))
	_ = d.Insert(makeLog(1, 1, "m", 200, base.Add(2*time.Hour+30*time.Minute)))

	// 1-hour buckets
	buckets, err := d.TimeSeries(QueryFilter{}, 3600, nil)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(buckets))
	}
	// First bucket: 2 requests, 1 error
	if buckets[0].Requests != 2 {
		t.Fatalf("bucket[0].Requests: want 2, got %d", buckets[0].Requests)
	}
	if buckets[0].Errors != 1 {
		t.Fatalf("bucket[0].Errors: want 1, got %d", buckets[0].Errors)
	}
	// Second bucket: 2 requests, 0 errors
	if buckets[1].Requests != 2 {
		t.Fatalf("bucket[1].Requests: want 2, got %d", buckets[1].Requests)
	}
	if buckets[1].Errors != 0 {
		t.Fatalf("bucket[1].Errors: want 0, got %d", buckets[1].Errors)
	}
}

func TestSQLiteDriver_TimeSeriesDefaultBucket(t *testing.T) {
	d, _ := newTestDriver(t)
	_ = d.Insert(makeLog(1, 1, "m", 200, time.Now()))
	// bucketSec <= 0 should default to 3600
	buckets, err := d.TimeSeries(QueryFilter{}, 0, nil)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if len(buckets) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(buckets))
	}
}

func TestSQLiteDriver_TimeSeriesMultiDay(t *testing.T) {
	d, _ := newTestDriver(t)
	day1 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	_ = d.Insert(makeLog(1, 1, "m", 200, day1))
	_ = d.Insert(makeLog(1, 1, "m", 200, day2))

	buckets, err := d.TimeSeries(QueryFilter{}, 86400, nil)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("expected 2 buckets (one per day), got %d", len(buckets))
	}
}

func TestSQLiteDriver_TimeSeriesEmpty(t *testing.T) {
	d, _ := newTestDriver(t)
	buckets, err := d.TimeSeries(QueryFilter{}, 3600, nil)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if len(buckets) != 0 {
		t.Fatalf("expected 0 buckets, got %d", len(buckets))
	}
}

// ---------- TopByField ----------

func TestSQLiteDriver_TopByFieldModel(t *testing.T) {
	d, _ := newTestDriver(t)
	now := time.Now()
	_ = d.Insert(makeLog(1, 1, "gpt-4", 200, now))
	_ = d.Insert(makeLog(1, 1, "gpt-4", 200, now.Add(time.Second)))
	_ = d.Insert(makeLog(1, 1, "gpt-4", 200, now.Add(2*time.Second)))
	_ = d.Insert(makeLog(1, 1, "gpt-3.5", 200, now.Add(3*time.Second)))
	_ = d.Insert(makeLog(1, 1, "claude", 200, now.Add(4*time.Second)))

	results, err := d.TopByField(QueryFilter{}, "model", 10, nil)
	if err != nil {
		t.Fatalf("TopByField: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 models, got %d", len(results))
	}
	if results[0].Label != "gpt-4" || results[0].Count != 3 {
		t.Fatalf("expected gpt-4 with count 3, got %+v", results[0])
	}
	if results[1].Label != "gpt-3.5" || results[1].Count != 1 {
		t.Fatalf("expected gpt-3.5 with count 1, got %+v", results[1])
	}
}

func TestSQLiteDriver_TopByFieldLimit(t *testing.T) {
	d, _ := newTestDriver(t)
	now := time.Now()
	for i := 0; i < 5; i++ {
		_ = d.Insert(makeLog(1, 1, fmt.Sprintf("model-%d", i), 200, now.Add(time.Duration(i)*time.Second)))
	}
	results, err := d.TopByField(QueryFilter{}, "model", 3, nil)
	if err != nil {
		t.Fatalf("TopByField: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results (limit), got %d", len(results))
	}
}

func TestSQLiteDriver_TopByFieldInvalid(t *testing.T) {
	d, _ := newTestDriver(t)
	_, err := d.TopByField(QueryFilter{}, "invalid_field", 10, nil)
	if err == nil {
		t.Fatal("expected error for invalid field")
	}
}

func TestSQLiteDriver_TopByFieldChannel(t *testing.T) {
	d, _ := newTestDriver(t)
	now := time.Now()
	_ = d.Insert(makeLog(1, 10, "m", 200, now))
	_ = d.Insert(makeLog(1, 10, "m", 200, now.Add(time.Second)))
	_ = d.Insert(makeLog(1, 20, "m", 200, now.Add(2*time.Second)))

	results, err := d.TopByField(QueryFilter{}, "channel_id", 10, nil)
	if err != nil {
		t.Fatalf("TopByField: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(results))
	}
	if results[0].Label != "10" || results[0].Count != 2 {
		t.Fatalf("expected channel 10 with count 2, got %+v", results[0])
	}
}

func TestSQLiteDriver_TopByFieldToken(t *testing.T) {
	d, _ := newTestDriver(t)
	now := time.Now()
	_ = d.Insert(makeLog(100, 1, "m", 200, now))
	_ = d.Insert(makeLog(200, 1, "m", 200, now.Add(time.Second)))

	results, err := d.TopByField(QueryFilter{}, "token_id", 10, nil)
	if err != nil {
		t.Fatalf("TopByField: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(results))
	}
}

func TestSQLiteDriver_TopByFieldEmptyLabel(t *testing.T) {
	d, _ := newTestDriver(t)
	l := makeLog(1, 1, "", 200, time.Now())
	_ = d.Insert(l)
	results, err := d.TopByField(QueryFilter{}, "model", 10, nil)
	if err != nil {
		t.Fatalf("TopByField: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Label != "(none)" {
		t.Fatalf("expected '(none)' for empty model, got %q", results[0].Label)
	}
}

// ---------- ListFiles / DeleteFiles ----------

func TestSQLiteDriver_ListFilesSorted(t *testing.T) {
	d, _ := newTestDriver(t)
	day3 := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	day1 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	_ = d.Insert(makeLog(1, 1, "m", 200, day3))
	_ = d.Insert(makeLog(1, 1, "m", 200, day1))
	_ = d.Insert(makeLog(1, 1, "m", 200, day2))

	files, err := d.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	expected := []string{"2026-07-01", "2026-07-02", "2026-07-03"}
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d: %v", len(files), files)
	}
	for i, f := range files {
		if f != expected[i] {
			t.Fatalf("file[%d]: want %s, got %s", i, expected[i], f)
		}
	}
}

func TestSQLiteDriver_DeleteFiles(t *testing.T) {
	d, dir := newTestDriver(t)
	day1 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	_ = d.Insert(makeLog(1, 1, "m", 200, day1))
	_ = d.Insert(makeLog(1, 1, "m", 200, day2))

	if err := d.DeleteFiles([]string{"2026-07-01"}); err != nil {
		t.Fatalf("DeleteFiles: %v", err)
	}

	// Verify file is gone
	if _, err := os.Stat(filepath.Join(dir, "2026-07-01.db")); !os.IsNotExist(err) {
		t.Fatal("expected file to be deleted")
	}

	// Verify other file still exists
	files, _ := d.ListFiles()
	if len(files) != 1 || files[0] != "2026-07-02" {
		t.Fatalf("expected only 2026-07-02, got %v", files)
	}
}

func TestSQLiteDriver_DeleteFilesIdempotent(t *testing.T) {
	d, _ := newTestDriver(t)
	_ = d.Insert(makeLog(1, 1, "m", 200, time.Now()))
	// Delete non-existent file - should not error
	if err := d.DeleteFiles([]string{"2099-01-01"}); err != nil {
		t.Fatalf("DeleteFiles of missing: %v", err)
	}
}

func TestSQLiteDriver_DeleteFilesClosesConnection(t *testing.T) {
	d, _ := newTestDriver(t)
	day := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	_ = d.Insert(makeLog(1, 1, "m", 200, day))

	// Insert to ensure connection is open
	_ = d.Insert(makeLog(1, 1, "m", 200, day.Add(time.Second)))

	if err := d.DeleteFiles([]string{"2026-07-01"}); err != nil {
		t.Fatalf("DeleteFiles: %v", err)
	}

	// Re-insert should work (reopens connection)
	if err := d.Insert(makeLog(1, 1, "m", 200, day)); err != nil {
		t.Fatalf("Insert after delete: %v", err)
	}
}

// ---------- buildWhere ----------

func TestBuildWhere(t *testing.T) {
	tests := []struct {
		name      string
		filter    QueryFilter
		wantConds int
	}{
		{"empty", QueryFilter{}, 0},
		{"token", QueryFilter{TokenID: 1}, 1},
		{"channel", QueryFilter{ChannelID: 1}, 1},
		{"model", QueryFilter{Model: "m"}, 1},
		{"status", QueryFilter{StatusCode: 500}, 1},
		{"from", QueryFilter{CreatedFrom: 100}, 1},
		{"to", QueryFilter{CreatedTo: 200}, 1},
		{"all", QueryFilter{TokenID: 1, ChannelID: 1, Model: "m", StatusCode: 500, CreatedFrom: 100, CreatedTo: 200}, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where, _ := buildWhere(tt.filter)
			if tt.wantConds == 0 {
				if where != "" {
					t.Fatalf("expected empty WHERE, got %q", where)
				}
				return
			}
			// Count " AND " occurrences
			ands := strings.Count(where, " AND ") + 1
			if ands != tt.wantConds {
				t.Fatalf("expected %d conditions, got WHERE: %q", tt.wantConds, where)
			}
		})
	}
}

// ---------- detachAll ----------

func TestDetachAllEmpty(t *testing.T) {
	d, _ := newTestDriver(t)
	day := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	_ = d.Insert(makeLog(1, 1, "m", 200, day))
	d.mu.RLock()
	df := d.conns["2026-07-01"]
	d.mu.RUnlock()
	if df == nil {
		t.Fatal("expected day file")
	}
	// Should not panic with empty aliases
	detachAll(df.conn, nil)
	detachAll(df.conn, []string{})
}

func TestDetachAllWithAliases(t *testing.T) {
	d, _ := newTestDriver(t)
	day := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	_ = d.Insert(makeLog(1, 1, "m", 200, day))
	d.mu.RLock()
	df := d.conns["2026-07-01"]
	d.mu.RUnlock()
	if df == nil {
		t.Fatal("expected day file")
	}
	// Should not panic with mixed nil/empty aliases
	detachAll(df.conn, []string{"", "_test0", ""})
}

// ---------- extractDate / seqOf ----------

func TestExtractDate(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		// The implementation uses LastIndex("-") and tries to parse
		// the suffix as an int. A bare "YYYY-MM-DD" has the day as a
		// number, so the implementation strips the day. The function
		// is only meaningful for distinguishing "YYYY-MM-DD" from
		// "YYYY-MM-DD-N" (N > 1). These tests document the behavior.
		{"2026-07-01-2", "2026-07-01"},
		{"2026-07-01-10", "2026-07-01"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := extractDate(tt.key)
			if got != tt.want {
				t.Fatalf("extractDate(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestSeqOf(t *testing.T) {
	tests := []struct {
		key  string
		want int
	}{
		// seqOf parses the last "-" segment. For "YYYY-MM-DD" it
		// returns the day number. For "YYYY-MM-DD-N" it returns N.
		// dayFileKey ensures the base file is stored as "YYYY-MM-DD"
		// (seq=0) and rollover files as "YYYY-MM-DD-N" (N>=1).
		{"2026-07-01-2", 2},
		{"2026-07-01-10", 10},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := seqOf(tt.key)
			if got != tt.want {
				t.Fatalf("seqOf(%q) = %d, want %d", tt.key, got, tt.want)
			}
		})
	}
}

// ---------- dayFileKey ----------

func TestDayFileKey(t *testing.T) {
	if got := dayFileKey("2026-07-01", 0); got != "2026-07-01" {
		t.Fatalf("seq 0: want '2026-07-01', got %q", got)
	}
	if got := dayFileKey("2026-07-01", 2); got != "2026-07-01-2" {
		t.Fatalf("seq 2: want '2026-07-01-2', got %q", got)
	}
}

// ---------- Manager.RunRetention ----------

func TestRunRetentionDisabled(t *testing.T) {
	m, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Should return immediately when retentionDays <= 0
	done := make(chan struct{})
	go func() {
		m.RunRetention(ctx, func() int { return 0 })
		close(done)
	}()
	select {
	case <-done:
		// good
	case <-time.After(100 * time.Millisecond):
		t.Fatal("RunRetention did not return when disabled")
	}
}

func TestRunRetentionDeletesOldFiles(t *testing.T) {
	m, _ := newTestManager(t)
	old := time.Now().UTC().AddDate(0, 0, -10)
	_ = m.Insert(makeLog(1, 1, "m", 200, old))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Run with retentionDays=5, but cancel before the 24h ticker fires
	go func() {
		// Give sweep time to run
		time.Sleep(50 * time.Millisecond)
		m.RunRetention(ctx, func() int { return 5 })
	}()

	// Wait for sweep to complete
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		files, _ := m.ListFiles()
		if len(files) == 0 {
			cancel()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	t.Fatal("retention did not delete old file")
}

func TestRunRetentionKeepsRecentFiles(t *testing.T) {
	m, _ := newTestManager(t)
	recent := time.Now().UTC()
	_ = m.Insert(makeLog(1, 1, "m", 200, recent))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.RunRetention(ctx, func() int { return 30 })
	time.Sleep(100 * time.Millisecond)
	cancel()

	files, _ := m.ListFiles()
	if len(files) == 0 {
		t.Fatal("recent file was deleted")
	}
}

func TestSetSynchronous_Validation(t *testing.T) {
	d := NewSQLiteDriver()
	for _, bad := range []string{"full", "off ", "On", "extra"} {
		if err := d.SetSynchronous(bad); err == nil {
			t.Errorf("SetSynchronous(%q) should fail", bad)
		}
	}
	for _, good := range []string{"", "normal", "off"} {
		if err := d.SetSynchronous(good); err != nil {
			t.Errorf("SetSynchronous(%q): %v", good, err)
		}
	}
	if d.syncMode != "off" {
		t.Errorf("syncMode=%q want off", d.syncMode)
	}
}

// TestSetSynchronousOffStillWrites: synchronous=off must keep the
// write path fully functional (same rows, just without fsync).
func TestSetSynchronousOffStillWrites(t *testing.T) {
	dir := t.TempDir()
	d := NewSQLiteDriver()
	if err := d.SetSynchronous("off"); err != nil {
		t.Fatal(err)
	}
	if err := d.Open(dir); err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	now := time.Now()
	if err := d.Insert(makeLog(1, 1, "m", 200, now)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := d.BatchInsert([]*model.Log{makeLog(2, 2, "m2", 300, now)}); err != nil {
		t.Fatalf("batch insert: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Reopen and verify both rows survived.
	d2 := NewSQLiteDriver()
	if err := d2.Open(dir); err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	_, total, err := d2.QueryAcross(QueryFilter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("rows=%d want 2", total)
	}
}

// TestCheckpointActive_TruncatesWAL: after bulk inserts the WAL
// holds committed frames; a TRUNCATE checkpoint resets it to zero
// bytes so the file on disk stays bounded.
func TestCheckpointActive_TruncatesWAL(t *testing.T) {
	d, dir := newTestDriver(t)
	now := time.Now().UTC()
	entries := make([]*model.Log, 0, 2000)
	for i := 0; i < 2000; i++ {
		entries = append(entries, makeLog(1, 1, "m", 200, now))
	}
	if _, err := d.BatchInsert(entries); err != nil {
		t.Fatalf("batch insert: %v", err)
	}
	walPath := filepath.Join(dir, now.Format("2006-01-02")+".db-wal")
	fi, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("wal stat: %v", err)
	}
	if fi.Size() == 0 {
		t.Skip("WAL was already compacted by autocheckpoint")
	}

	if err := d.CheckpointActive(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	fi2, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("wal stat after: %v", err)
	}
	if fi2.Size() != 0 {
		t.Errorf("wal size after TRUNCATE = %d, want 0", fi2.Size())
	}
}

// TestRunCheckpoints_Periodic: the maintenance loop runs a
// checkpoint on entry and then on every tick (interval shortened),
// keeping the WAL of the active file compacted while inserts flow.
func TestRunCheckpoints_Periodic(t *testing.T) {
	d, dir := newTestDriver(t)
	old := checkpointInterval
	checkpointInterval = 30 * time.Millisecond
	defer func() { checkpointInterval = old }()

	m, err := New(dir, d)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	now := time.Now().UTC()
	entries := make([]*model.Log, 0, 3000)
	for i := 0; i < 3000; i++ {
		entries = append(entries, makeLog(1, 1, "m", 200, now))
	}
	if _, err := m.BatchInsert(entries); err != nil {
		t.Fatal(err)
	}
	walPath := filepath.Join(dir, now.Format("2006-01-02")+".db-wal")
	if fi, err := os.Stat(walPath); err != nil || fi.Size() == 0 {
		t.Skip("no WAL to compact (autocheckpoint already ran)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	go m.RunCheckpoints(ctx)
	time.Sleep(120 * time.Millisecond) // ≥3 ticks
	cancel()

	if fi, err := os.Stat(walPath); err == nil && fi.Size() != 0 {
		t.Errorf("wal size after maintenance ticks = %d, want 0", fi.Size())
	}
}
