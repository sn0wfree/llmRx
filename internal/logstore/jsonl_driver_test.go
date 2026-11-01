package logstore

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// newJSONLDriver returns an opened JSONL driver rooted at a temp dir.
func newJSONLDriver(t *testing.T) (*JSONLDriver, string) {
	t.Helper()
	dir := t.TempDir()
	d := NewJSONLDriver()
	if err := d.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, dir
}

func jsonlLog(tokenID, channelID int64, modelName string, status int, created time.Time) *model.Log {
	return &model.Log{
		TokenID:          tokenID,
		ChannelID:        channelID,
		KeyID:            channelID * 10,
		Model:            modelName,
		PromptTokens:     int(created.Unix()%97 + 1),
		CompletionTokens: int(created.Unix()%31 + 1),
		RealCostUSD:      0.001 * float64(created.Unix()%13+1),
		BilledCostUSD:    0.002 * float64(created.Unix()%17+1),
		DurationMs:       42,
		StatusCode:       status,
		RouterPath:       "static",
		RequestIP:        "127.0.0.1",
		Endpoint:         "",
		Units:            1,
		CreatedAt:        created,
	}
}

// ---------- write path ----------

func TestJSONL_OpenCreatesDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "nested", "logs")
	d := NewJSONLDriver()
	if err := d.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("dir not created: %v", err)
	}
}

func TestJSONL_OpenEmptyDir(t *testing.T) {
	if err := NewJSONLDriver().Open(""); err == nil {
		t.Fatal("want error for empty dir")
	}
}

func TestJSONL_InsertRoutesToDayFile(t *testing.T) {
	d, dir := newJSONLDriver(t)
	ts := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	if err := d.Insert(jsonlLog(1, 1, "m", 200, ts)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	path := filepath.Join(dir, "2026-03-14.jsonl")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("day file missing: %v", err)
	}
	if fi.Size() == 0 {
		t.Fatal("day file empty")
	}
}

func TestJSONL_InsertZeroCreatedAt(t *testing.T) {
	d, _ := newJSONLDriver(t)
	e := jsonlLog(1, 1, "m", 200, time.Time{})
	if err := d.Insert(e); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if e.CreatedAt.IsZero() {
		t.Fatal("CreatedAt not defaulted")
	}
}

// A batch that crosses UTC midnight must land in two files, otherwise
// rows are silently attributed to the wrong day.
func TestJSONL_BatchInsertSplitsAcrossDays(t *testing.T) {
	d, dir := newJSONLDriver(t)
	before := time.Date(2026, 5, 1, 23, 59, 0, 0, time.UTC)
	after := time.Date(2026, 5, 2, 0, 1, 0, 0, time.UTC)
	entries := []*model.Log{
		jsonlLog(1, 1, "m", 200, before),
		jsonlLog(1, 1, "m", 200, before),
		jsonlLog(1, 1, "m", 200, after),
	}
	n, err := d.BatchInsert(entries)
	if err != nil {
		t.Fatalf("BatchInsert: %v", err)
	}
	if n != 3 {
		t.Fatalf("inserted %d, want 3", n)
	}
	for _, want := range []string{"2026-05-01.jsonl", "2026-05-02.jsonl"} {
		path := filepath.Join(dir, want)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s missing: %v", want, err)
		}
	}
}

func TestJSONL_BatchInsertEmpty(t *testing.T) {
	d, _ := newJSONLDriver(t)
	n, err := d.BatchInsert(nil)
	if err != nil || n != 0 {
		t.Fatalf("got n=%d err=%v", n, err)
	}
}

func TestJSONL_RolloverCreatesSeqFiles(t *testing.T) {
	old := MaxFileBytes
	MaxFileBytes = 512 // tiny, forces rollover quickly
	defer func() { MaxFileBytes = old }()

	d, dir := newJSONLDriver(t)
	ts := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 60; i++ {
		if err := d.Insert(jsonlLog(int64(i), 1, "some-long-model-name", 200, ts)); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	files, err := d.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) < 2 {
		t.Fatalf("expected rollover into multiple files, got %v", files)
	}
	// Every row must still be readable across the seq files.
	_, total, err := d.QueryAcross(QueryFilter{Limit: 100}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 60 {
		t.Fatalf("total=%d, want 60 (files=%v dir=%s)", total, files, dir)
	}
}

func TestJSONL_InsertConcurrent(t *testing.T) {
	d, _ := newJSONLDriver(t)
	ts := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func(g int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 50; i++ {
				if err := d.Insert(jsonlLog(int64(g), 1, "m", 200, ts)); err != nil {
					t.Errorf("Insert: %v", err)
					return
				}
			}
		}(g)
	}
	for g := 0; g < 8; g++ {
		<-done
	}
	_, total, err := d.QueryAcross(QueryFilter{Limit: 1}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 400 {
		t.Fatalf("total=%d, want 400", total)
	}
}

// A row written by another process (no in-memory counter) must be
// visible after Close/Open because size is re-read from disk.
func TestJSONL_ReopenSeesExistingRows(t *testing.T) {
	dir := t.TempDir()
	d := NewJSONLDriver()
	if err := d.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	ts := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	if err := d.Insert(jsonlLog(1, 1, "m", 200, ts)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	d2 := NewJSONLDriver()
	if err := d2.Open(dir); err != nil {
		t.Fatalf("Open2: %v", err)
	}
	defer d2.Close()
	_, total, err := d2.QueryAcross(QueryFilter{Limit: 10}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 1 {
		t.Fatalf("total=%d, want 1", total)
	}
}

// ---------- read path ----------

func TestJSONL_QueryOrderNewestFirst(t *testing.T) {
	d, _ := newJSONLDriver(t)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := d.Insert(jsonlLog(1, 1, "m", 200, base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	rows, total, err := d.QueryAcross(QueryFilter{Limit: 10}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 5 || len(rows) != 5 {
		t.Fatalf("total=%d len=%d", total, len(rows))
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].CreatedAt.After(rows[i-1].CreatedAt) {
			t.Fatalf("rows not newest-first: %v after %v", rows[i].CreatedAt, rows[i-1].CreatedAt)
		}
	}
}

func TestJSONL_QueryPagination(t *testing.T) {
	d, _ := newJSONLDriver(t)
	base := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		if err := d.Insert(jsonlLog(1, 1, "m", 200, base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	page1, total, err := d.QueryAcross(QueryFilter{Limit: 3, Offset: 0}, nil)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if total != 10 || len(page1) != 3 {
		t.Fatalf("page1 total=%d len=%d", total, len(page1))
	}
	page2, _, err := d.QueryAcross(QueryFilter{Limit: 3, Offset: 3}, nil)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 3 {
		t.Fatalf("page2 len=%d", len(page2))
	}
	// Pages must not overlap.
	seen := map[time.Time]bool{}
	for _, r := range page1 {
		seen[r.CreatedAt] = true
	}
	for _, r := range page2 {
		if seen[r.CreatedAt] {
			t.Fatalf("duplicate row across pages: %v", r.CreatedAt)
		}
	}
}

func TestJSONL_QueryEmpty(t *testing.T) {
	d, _ := newJSONLDriver(t)
	rows, total, err := d.QueryAcross(QueryFilter{}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 0 || len(rows) != 0 {
		t.Fatalf("total=%d len=%d", total, len(rows))
	}
}

func TestJSONL_SkipsMalformedLines(t *testing.T) {
	d, dir := newJSONLDriver(t)
	ts := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := d.Insert(jsonlLog(1, 1, "m", 200, ts)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Append a half-written row, as a crash would leave behind.
	f, err := os.OpenFile(filepath.Join(dir, "2026-10-01.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString(`{"token_id":9,"model":"trunc`); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = f.Close()

	rows, total, err := d.QueryAcross(QueryFilter{Limit: 10}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("total=%d len=%d, want 1 (malformed line should be skipped)", total, len(rows))
	}
}

func TestJSONL_QuerySpecificDays(t *testing.T) {
	d, _ := newJSONLDriver(t)
	for _, day := range []int{1, 2, 3} {
		ts := time.Date(2026, 11, day, 0, 0, 0, 0, time.UTC)
		if err := d.Insert(jsonlLog(1, 1, fmt.Sprintf("m%d", day), 200, ts)); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	_, total, err := d.QueryAcross(QueryFilter{Limit: 10}, []string{"2026-11-02"})
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 1 {
		t.Fatalf("total=%d, want 1", total)
	}
}

// ---------- filters ----------

func TestJSONL_FilterSemantics(t *testing.T) {
	d, _ := newJSONLDriver(t)
	ts := time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)

	ok := jsonlLog(7, 3, "gpt-4", 200, ts)
	errRow := jsonlLog(8, 3, "gpt-4", 500, ts)
	other := jsonlLog(7, 4, "claude-3", 200, ts)
	hit := jsonlLog(7, 3, "gpt-4", 200, ts)
	hit.CacheHit = true
	mcp := jsonlLog(7, 3, "gpt-4", 200, ts)
	mcp.Endpoint = "mcp"

	for _, e := range []*model.Log{ok, errRow, other, hit, mcp} {
		if err := d.Insert(e); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	cases := []struct {
		name   string
		filter QueryFilter
		want   int64
	}{
		{"no filter", QueryFilter{}, 5},
		{"token", QueryFilter{TokenID: 7}, 4},
		{"token multi", QueryFilter{TokenIDs: []int64{7, 8}}, 5},
		{"channel", QueryFilter{ChannelID: 4}, 1},
		{"key", QueryFilter{KeyID: 30}, 4},
		{"model", QueryFilter{Model: "claude-3"}, 1},
		{"status", QueryFilter{StatusCode: 500}, 1},
		{"cache hit", QueryFilter{CacheHit: true}, 1},
		{"endpoint", QueryFilter{Endpoint: "mcp"}, 1},
		{"from", QueryFilter{CreatedFrom: ts.Unix() + 1}, 0},
		{"from inclusive", QueryFilter{CreatedFrom: ts.Unix()}, 5},
		{"to exclusive", QueryFilter{CreatedTo: ts.Unix() - 1}, 0},
		{"to inclusive", QueryFilter{CreatedTo: ts.Unix()}, 5},
		{"combined", QueryFilter{TokenID: 7, Model: "gpt-4", StatusCode: 200}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.filter.Limit = 100
			_, total, err := d.QueryAcross(tc.filter, nil)
			if err != nil {
				t.Fatalf("QueryAcross: %v", err)
			}
			if total != tc.want {
				t.Fatalf("total=%d, want %d", total, tc.want)
			}
		})
	}
}

// ---------- stats / series / top ----------

func TestJSONL_LogStats(t *testing.T) {
	d, _ := newJSONLDriver(t)
	ts := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	a := jsonlLog(1, 1, "m", 200, ts)
	b := jsonlLog(1, 1, "m", 500, ts)
	for _, e := range []*model.Log{a, b} {
		if err := d.Insert(e); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	got, err := d.LogStats(nil)
	if err != nil {
		t.Fatalf("LogStats: %v", err)
	}
	if got.Total != 2 {
		t.Fatalf("Total=%d, want 2", got.Total)
	}
	if got.Errors != 1 {
		t.Fatalf("Errors=%d, want 1", got.Errors)
	}
	wantPrompt := int64(a.PromptTokens + b.PromptTokens)
	if got.PromptTokens != wantPrompt {
		t.Fatalf("PromptTokens=%d, want %d", got.PromptTokens, wantPrompt)
	}
	wantReal := a.RealCostUSD + b.RealCostUSD
	if diff := got.RealCostUSD - wantReal; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("RealCostUSD=%v, want %v", got.RealCostUSD, wantReal)
	}
}

func TestJSONL_LogStatsEmpty(t *testing.T) {
	d, _ := newJSONLDriver(t)
	got, err := d.LogStats(nil)
	if err != nil {
		t.Fatalf("LogStats: %v", err)
	}
	if got.Total != 0 {
		t.Fatalf("Total=%d, want 0", got.Total)
	}
}

// A data file without its .meta.json (fresh upgrade or crash) must
// still produce correct aggregates by rebuilding.
func TestJSONL_LogStatsRebuildsMissingMeta(t *testing.T) {
	d, dir := newJSONLDriver(t)
	ts := time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := d.Insert(jsonlLog(1, 1, "m", 200, ts)); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	if err := os.Remove(filepath.Join(dir, "2027-02-01"+metaExt)); err != nil {
		t.Fatalf("remove meta: %v", err)
	}
	got, err := d.LogStats(nil)
	if err != nil {
		t.Fatalf("LogStats: %v", err)
	}
	if got.Total != 3 {
		t.Fatalf("Total=%d, want 3", got.Total)
	}
}

func TestJSONL_TimeSeries(t *testing.T) {
	d, _ := newJSONLDriver(t)
	base := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		status := 200
		if i == 3 {
			status = 500
		}
		if err := d.Insert(jsonlLog(1, 1, "m", status, base.Add(time.Duration(i)*30*time.Minute))); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	series, err := d.TimeSeries(QueryFilter{}, 3600, nil)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if len(series) != 2 {
		t.Fatalf("buckets=%d, want 2", len(series))
	}
	var requests, errors int64
	for _, b := range series {
		requests += b.Requests
		errors += b.Errors
	}
	if requests != 4 || errors != 1 {
		t.Fatalf("requests=%d errors=%d", requests, errors)
	}
	if series[0].Bucket >= series[1].Bucket {
		t.Fatal("buckets not ascending")
	}
}

func TestJSONL_TimeSeriesDefaultBucket(t *testing.T) {
	d, _ := newJSONLDriver(t)
	ts := time.Date(2027, 4, 1, 5, 17, 0, 0, time.UTC)
	if err := d.Insert(jsonlLog(1, 1, "m", 200, ts)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	series, err := d.TimeSeries(QueryFilter{}, 0, nil)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if len(series) != 1 {
		t.Fatalf("buckets=%d", len(series))
	}
	if series[0].Bucket%3600 != 0 {
		t.Fatalf("bucket %d not aligned to 3600", series[0].Bucket)
	}
}

func TestJSONL_TopByFieldModel(t *testing.T) {
	d, _ := newJSONLDriver(t)
	ts := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := d.Insert(jsonlLog(1, 1, "gpt-4", 200, ts)); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	if err := d.Insert(jsonlLog(1, 1, "claude-3", 200, ts)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	out, err := d.TopByField(QueryFilter{}, "model", 10, nil)
	if err != nil {
		t.Fatalf("TopByField: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("groups=%d, want 2", len(out))
	}
	if out[0].Label != "gpt-4" || out[0].Count != 3 {
		t.Fatalf("first group = %+v", out[0])
	}
}

func TestJSONL_TopByFieldChannelAndToken(t *testing.T) {
	d, _ := newJSONLDriver(t)
	ts := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if err := d.Insert(jsonlLog(5, 9, "m", 200, ts)); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	byCh, err := d.TopByField(QueryFilter{}, "channel_id", 5, nil)
	if err != nil {
		t.Fatalf("TopByField channel: %v", err)
	}
	if len(byCh) != 1 || byCh[0].Label != "9" || byCh[0].Count != 2 {
		t.Fatalf("by channel = %+v", byCh)
	}
	byTok, err := d.TopByField(QueryFilter{}, "token_id", 5, nil)
	if err != nil {
		t.Fatalf("TopByField token: %v", err)
	}
	if len(byTok) != 1 || byTok[0].Label != "5" {
		t.Fatalf("by token = %+v", byTok)
	}
}

func TestJSONL_TopByFieldFiltered(t *testing.T) {
	d, _ := newJSONLDriver(t)
	ts := time.Date(2027, 7, 1, 0, 0, 0, 0, time.UTC)
	if err := d.Insert(jsonlLog(1, 1, "a", 200, ts)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := d.Insert(jsonlLog(1, 1, "b", 500, ts)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	out, err := d.TopByField(QueryFilter{StatusCode: 200}, "model", 10, nil)
	if err != nil {
		t.Fatalf("TopByField: %v", err)
	}
	if len(out) != 1 || out[0].Label != "a" {
		t.Fatalf("got %+v", out)
	}
}

func TestJSONL_TopByFieldInvalidField(t *testing.T) {
	d, _ := newJSONLDriver(t)
	if _, err := d.TopByField(QueryFilter{}, "nope", 5, nil); err == nil {
		t.Fatal("want error for invalid field")
	}
}

func TestJSONL_TopByFieldEmptyLabel(t *testing.T) {
	d, _ := newJSONLDriver(t)
	ts := time.Date(2027, 8, 1, 0, 0, 0, 0, time.UTC)
	if err := d.Insert(jsonlLog(1, 1, "", 200, ts)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	out, err := d.TopByField(QueryFilter{}, "model", 5, nil)
	if err != nil {
		t.Fatalf("TopByField: %v", err)
	}
	if len(out) != 1 || out[0].Label != "(none)" {
		t.Fatalf("got %+v", out)
	}
}

// ---------- file management ----------

func TestJSONL_ListFiles(t *testing.T) {
	d, _ := newJSONLDriver(t)
	for _, day := range []int{3, 1, 2} {
		ts := time.Date(2027, 9, day, 0, 0, 0, 0, time.UTC)
		if err := d.Insert(jsonlLog(1, 1, "m", 200, ts)); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	files, err := d.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	want := []string{"2027-09-01", "2027-09-02", "2027-09-03"}
	if len(files) != len(want) {
		t.Fatalf("files=%v", files)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("files=%v, want %v", files, want)
		}
	}
}

// The meta rollup must never be returned as a log file.
func TestJSONL_ListFilesExcludesMeta(t *testing.T) {
	d, dir := newJSONLDriver(t)
	ts := time.Date(2027, 9, 9, 0, 0, 0, 0, time.UTC)
	if err := d.Insert(jsonlLog(1, 1, "m", 200, ts)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2027-09-09"+metaExt)); err != nil {
		t.Fatalf("meta not written: %v", err)
	}
	files, err := d.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	for _, f := range files {
		if f != "2027-09-09" {
			t.Fatalf("unexpected file in listing: %q (all=%v)", f, files)
		}
	}
}

func TestJSONL_DeleteFiles(t *testing.T) {
	d, dir := newJSONLDriver(t)
	ts := time.Date(2027, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := d.Insert(jsonlLog(1, 1, "m", 200, ts)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := d.DeleteFiles([]string{"2027-10-01"}); err != nil {
		t.Fatalf("DeleteFiles: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2027-10-01.jsonl")); !os.IsNotExist(err) {
		t.Fatal("data file not removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "2027-10-01"+metaExt)); !os.IsNotExist(err) {
		t.Fatal("meta file not removed")
	}
	// Idempotent.
	if err := d.DeleteFiles([]string{"2027-10-01"}); err != nil {
		t.Fatalf("DeleteFiles idempotent: %v", err)
	}
}

func TestJSONL_ArchiveFiles(t *testing.T) {
	d, dir := newJSONLDriver(t)
	ts := time.Date(2027, 11, 1, 0, 0, 0, 0, time.UTC)
	if err := d.Insert(jsonlLog(1, 1, "m", 200, ts)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := d.ArchiveFiles([]string{"2027-11-01"}); err != nil {
		t.Fatalf("ArchiveFiles: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2027-11-01.jsonl")); !os.IsNotExist(err) {
		t.Fatal("original not removed after archive")
	}
	if _, err := os.Stat(filepath.Join(dir, "2027-11-01.jsonl.gz")); err != nil {
		t.Fatalf("archive missing: %v", err)
	}
	// Archived days must drop out of listings so retention and admin
	// queries see the same file set.
	files, err := d.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("archived file still listed: %v", files)
	}
	// Idempotent.
	if err := d.ArchiveFiles([]string{"2027-11-01"}); err != nil {
		t.Fatalf("ArchiveFiles idempotent: %v", err)
	}
}

func TestJSONL_CloseIdempotent(t *testing.T) {
	dir := t.TempDir()
	d := NewJSONLDriver()
	if err := d.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	ts := time.Date(2027, 12, 1, 0, 0, 0, 0, time.UTC)
	if err := d.Insert(jsonlLog(1, 1, "m", 200, ts)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// Insert after Close must not panic.
	_ = d.Insert(jsonlLog(1, 1, "m", 200, ts))
}

// ---------- manager integration ----------

func TestJSONL_ManagerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, NewJSONLDriver())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()

	ts := time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := m.Insert(jsonlLog(1, 1, "m", 200, ts.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	m.Flush()

	rows, total, err := m.Query(QueryFilter{Limit: 10}, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 5 || len(rows) != 5 {
		t.Fatalf("total=%d len=%d", total, len(rows))
	}
	stats, err := m.Stats(nil)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Total != 5 {
		t.Fatalf("stats.Total=%d", stats.Total)
	}
}
