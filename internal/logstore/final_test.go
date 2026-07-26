package logstore

import (
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestInsert_MultipleDays(t *testing.T) {
	d, _ := newTestDriver(t)
	now := time.Now().UTC()
	yesterday := now.AddDate(0, 0, -1)
	d.Insert(makeLog(1, 1, "m", 200, yesterday))
	d.Insert(makeLog(1, 1, "m", 200, now))
	logs, total, err := d.QueryAcross(QueryFilter{Limit: 100}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 2 || len(logs) != 2 {
		t.Errorf("expected 2 logs, got total=%d len=%d", total, len(logs))
	}
}

func TestLogStats_MultiDay(t *testing.T) {
	d, _ := newTestDriver(t)
	now := time.Now().UTC()
	yesterday := now.AddDate(0, 0, -1)
	d.Insert(makeLog(1, 1, "m", 200, yesterday))
	d.Insert(makeLog(1, 1, "m", 500, now))
	r, err := d.LogStats(nil)
	if err != nil {
		t.Fatalf("LogStats: %v", err)
	}
	if r.Total != 2 {
		t.Errorf("total=%d want 2", r.Total)
	}
	if r.Errors != 1 {
		t.Errorf("errors=%d want 1", r.Errors)
	}
}

func TestTopByField_WithData(t *testing.T) {
	d, _ := newTestDriver(t)
	now := time.Now().UTC()
	d.Insert(makeLog(1, 1, "gpt-4", 200, now))
	d.Insert(makeLog(1, 1, "gpt-4", 200, now))
	d.Insert(makeLog(1, 1, "claude-3", 200, now))
	out, err := d.TopByField(QueryFilter{Limit: 100}, "model", 10, nil)
	if err != nil {
		t.Fatalf("TopByField: %v", err)
	}
	if len(out) < 2 {
		t.Errorf("expected at least 2 models, got %d", len(out))
	}
	if out[0].Label != "gpt-4" {
		t.Errorf("top model should be gpt-4, got %s", out[0].Label)
	}
}

func TestQueryAcross_WithModelFilter(t *testing.T) {
	d, _ := newTestDriver(t)
	now := time.Now().UTC()
	d.Insert(makeLog(1, 1, "gpt-4", 200, now))
	d.Insert(makeLog(1, 1, "claude-3", 200, now))
	logs, total, err := d.QueryAcross(QueryFilter{Limit: 100, Model: "gpt-4"}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 1 || len(logs) != 1 {
		t.Errorf("expected 1 gpt-4 log, got total=%d", total)
	}
}

func TestQueryAcross_StatusFilter(t *testing.T) {
	d, _ := newTestDriver(t)
	now := time.Now().UTC()
	d.Insert(makeLog(1, 1, "m", 200, now))
	d.Insert(makeLog(1, 1, "m", 500, now))
	logs, total, err := d.QueryAcross(QueryFilter{Limit: 100, StatusCode: 500}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 1 || len(logs) != 1 {
		t.Errorf("expected 1 error log, got total=%d", total)
	}
}

func TestListFiles_AfterInserts(t *testing.T) {
	d, _ := newTestDriver(t)
	now := time.Now().UTC()
	yesterday := now.AddDate(0, 0, -1)
	d.Insert(makeLog(1, 1, "m", 200, yesterday))
	d.Insert(makeLog(1, 1, "m", 200, now))
	files, err := d.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) < 2 {
		t.Errorf("expected at least 2 files, got %d", len(files))
	}
}

func TestDeleteFiles_ExistingFile(t *testing.T) {
	d, _ := newTestDriver(t)
	now := time.Now().UTC()
	d.Insert(makeLog(1, 1, "m", 200, now))
	files, _ := d.ListFiles()
	if len(files) == 0 {
		t.Fatal("expected at least 1 file")
	}
	if err := d.DeleteFiles(files); err != nil {
		t.Fatalf("DeleteFiles: %v", err)
	}
	files2, _ := d.ListFiles()
	if len(files2) != 0 {
		t.Errorf("expected 0 files after delete, got %d", len(files2))
	}
}

func TestInsert_WithZeroCreatedAt(t *testing.T) {
	d, _ := newTestDriver(t)
	entry := &model.Log{Model: "m", StatusCode: 200, CreatedAt: time.Time{}}
	if err := d.Insert(entry); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if entry.CreatedAt.IsZero() {
		t.Errorf("CreatedAt should be set")
	}
}
