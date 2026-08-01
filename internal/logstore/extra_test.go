package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestInsert_ZeroCreatedAt(t *testing.T) {
	d, _ := newTestDriver(t)
	entry := &model.Log{
		Model:        "gpt-4",
		PromptTokens: 10,
		StatusCode:   200,
		CreatedAt:    time.Time{},
	}
	if err := d.Insert(entry); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if entry.CreatedAt.IsZero() {
		t.Errorf("CreatedAt should be set by Insert")
	}
}

func TestLogStats_Empty(t *testing.T) {
	d, _ := newTestDriver(t)
	r, err := d.LogStats(nil)
	if err != nil {
		t.Fatalf("LogStats: %v", err)
	}
	if r.Total != 0 {
		t.Errorf("total=%d want 0", r.Total)
	}
}

func TestLogStats_WithErrors(t *testing.T) {
	d, _ := newTestDriver(t)
	now := time.Now().UTC()
	d.Insert(makeLog(1, 1, "m", 200, now))
	d.Insert(makeLog(1, 1, "m", 500, now))
	d.Insert(makeLog(1, 1, "m", 200, now))

	r, err := d.LogStats(nil)
	if err != nil {
		t.Fatalf("LogStats: %v", err)
	}
	if r.Total != 3 {
		t.Errorf("total=%d want 3", r.Total)
	}
	if r.Errors != 1 {
		t.Errorf("errors=%d want 1", r.Errors)
	}
}

func TestTopByField_Empty(t *testing.T) {
	d, _ := newTestDriver(t)
	out, err := d.TopByField(QueryFilter{Limit: 10}, "model", 10, nil)
	if err != nil {
		t.Fatalf("TopByField: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty, got %d", len(out))
	}
}

func TestTimeSeries_Empty(t *testing.T) {
	d, _ := newTestDriver(t)
	out, err := d.TimeSeries(QueryFilter{Limit: 10}, 3600, nil)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty, got %d", len(out))
	}
}

func TestQueryAcross_Empty(t *testing.T) {
	d, _ := newTestDriver(t)
	out, total, err := d.QueryAcross(QueryFilter{Limit: 10}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 0 || len(out) != 0 {
		t.Errorf("expected empty results")
	}
}

func TestOpenUnion_Empty(t *testing.T) {
	d, _ := newTestDriver(t)
	df, aliases, days, err := d.openUnion(nil)
	if err != nil {
		t.Fatalf("openUnion: %v", err)
	}
	if df != nil || len(aliases) != 0 || len(days) != 0 {
		t.Errorf("expected nil/empty for no data")
	}
}

func TestRunRetention_Disabled(t *testing.T) {
	dir := t.TempDir()
	ls, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer ls.Close()
	ls.RunRetention(context.Background(), func() int { return 0 })
}

func TestRunRetention_WithSweep(t *testing.T) {
	dir := t.TempDir()
	ls, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer ls.Close()

	old := time.Now().UTC().AddDate(0, 0, -10)
	ls.Insert(&model.Log{Model: "m", StatusCode: 200, CreatedAt: old})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ls.RunRetention(ctx, func() int { return 1 })
}

func TestDeleteFiles_Nonexistent(t *testing.T) {
	d, _ := newTestDriver(t)
	err := d.DeleteFiles([]string{"nonexistent-2020-01-01"})
	if err != nil {
		t.Errorf("DeleteFiles with nonexistent should not error: %v", err)
	}
}

func TestListFiles_Empty(t *testing.T) {
	d, _ := newTestDriver(t)
	files, err := d.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected empty, got %v", files)
	}
}

func TestClose_Idempotent(t *testing.T) {
	d, _ := newTestDriver(t)
	if err := d.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestManager_Stats(t *testing.T) {
	dir := t.TempDir()
	ls, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer ls.Close()

	now := time.Now().UTC()
	ls.Insert(makeLog(1, 1, "m", 200, now))
	ls.Insert(makeLog(1, 1, "m", 500, now))
	ls.Flush()

	r, err := ls.Stats(nil)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if r.Total != 2 {
		t.Errorf("total=%d want 2", r.Total)
	}
	if r.Errors != 1 {
		t.Errorf("errors=%d want 1", r.Errors)
	}
}
