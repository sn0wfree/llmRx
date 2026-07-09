package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/model"
)

func openTempWithLogs(t *testing.T) *SQLite {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")
	s, err := OpenSQLite(dsn)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	logDir := filepath.Join(dir, "logs")
	if err := logstore.EnsureDir(logDir); err != nil {
		t.Fatalf("logstore.EnsureDir: %v", err)
	}
	logStore, err := logstore.New(logDir, nil)
	if err != nil {
		t.Fatalf("logstore.New: %v", err)
	}
	s.SetLogStore(logStore)
	t.Cleanup(func() { _ = logStore.Close() })

	return s
}

// ---------- CreateLog ----------

func TestCreateLogWithoutLogStore(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")
	s, err := OpenSQLite(dsn)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer s.Close()

	err = s.CreateLog(&model.Log{Model: "m", StatusCode: 200})
	if err == nil {
		t.Fatal("expected error when logstore not initialized")
	}
}

func TestCreateLogSetsCreatedAt(t *testing.T) {
	s := openTempWithLogs(t)
	l := &model.Log{Model: "m", StatusCode: 200}
	if err := s.CreateLog(l); err != nil {
		t.Fatalf("CreateLog: %v", err)
	}
	if l.CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt to be set")
	}
}

func TestCreateLogMultipleDays(t *testing.T) {
	s := openTempWithLogs(t)
	day1 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	day3 := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)

	for _, day := range []time.Time{day1, day2, day3} {
		if err := s.CreateLog(&model.Log{Model: "m", StatusCode: 200, CreatedAt: day}); err != nil {
			t.Fatalf("CreateLog: %v", err)
		}
	}

	files, _ := s.logStore.ListFiles()
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(files))
	}
}

// ---------- GetLogs ----------

func TestGetLogsEmpty(t *testing.T) {
	s := openTempWithLogs(t)
	logs, err := s.GetLogs(10, 0)
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("expected 0 logs, got %d", len(logs))
	}
}

func TestGetLogsPagination(t *testing.T) {
	s := openTempWithLogs(t)
	now := time.Now()
	for i := 0; i < 15; i++ {
		_ = s.CreateLog(&model.Log{Model: "m", StatusCode: 200, CreatedAt: now.Add(time.Duration(i) * time.Second)})
	}

	logs, err := s.GetLogs(5, 0)
	if err != nil {
		t.Fatalf("GetLogs page1: %v", err)
	}
	if len(logs) != 5 {
		t.Fatalf("page1: expected 5, got %d", len(logs))
	}

	logs, err = s.GetLogs(5, 10)
	if err != nil {
		t.Fatalf("GetLogs page3: %v", err)
	}
	if len(logs) != 5 {
		t.Fatalf("page3: expected 5, got %d", len(logs))
	}
}

func TestGetLogsLimitBounds(t *testing.T) {
	s := openTempWithLogs(t)
	now := time.Now()
	_ = s.CreateLog(&model.Log{Model: "m", StatusCode: 200, CreatedAt: now})

	// Limit > 500 should be capped at 500
	logs, err := s.GetLogs(1000, 0)
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}

	// Limit <= 0 should default to 50
	logs, err = s.GetLogs(0, 0)
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
}

// ---------- CountLogs ----------

func TestCountLogs(t *testing.T) {
	s := openTempWithLogs(t)
	now := time.Now()
	for i := 0; i < 5; i++ {
		_ = s.CreateLog(&model.Log{Model: "m", StatusCode: 200, CreatedAt: now.Add(time.Duration(i) * time.Second)})
	}
	n, err := s.CountLogs()
	if err != nil {
		t.Fatalf("CountLogs: %v", err)
	}
	if n != 5 {
		t.Fatalf("expected 5, got %d", n)
	}
}

func TestCountLogsEmpty(t *testing.T) {
	s := openTempWithLogs(t)
	n, err := s.CountLogs()
	if err != nil {
		t.Fatalf("CountLogs: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0, got %d", n)
	}
}

// ---------- DeleteLogsBefore ----------

func TestDeleteLogsBefore(t *testing.T) {
	s := openTempWithLogs(t)
	old := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Now()
	_ = s.CreateLog(&model.Log{Model: "m", StatusCode: 200, CreatedAt: old})
	_ = s.CreateLog(&model.Log{Model: "m", StatusCode: 200, CreatedAt: recent})

	// Cutoff: delete anything before 2026-07-01
	cutoff := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC).Unix()
	n, err := s.DeleteLogsBefore(cutoff)
	if err != nil {
		t.Fatalf("DeleteLogsBefore: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 file deleted, got %d", n)
	}

	// Verify old file is gone
	files, _ := s.logStore.ListFiles()
	if len(files) != 1 {
		t.Fatalf("expected 1 file remaining, got %d", len(files))
	}
}

func TestDeleteLogsBeforeNoMatch(t *testing.T) {
	s := openTempWithLogs(t)
	now := time.Now()
	_ = s.CreateLog(&model.Log{Model: "m", StatusCode: 200, CreatedAt: now})

	cutoff := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	n, err := s.DeleteLogsBefore(cutoff)
	if err != nil {
		t.Fatalf("DeleteLogsBefore: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 files deleted, got %d", n)
	}
}

// ---------- LogStats ----------

func TestLogStatsAggregates(t *testing.T) {
	s := openTempWithLogs(t)
	now := time.Now()
	_ = s.CreateLog(&model.Log{Model: "m", StatusCode: 200, PromptTokens: 100, CompletionTokens: 50, RealCostUSD: 0.01, BilledCostUSD: 0.015, CreatedAt: now})
	_ = s.CreateLog(&model.Log{Model: "m", StatusCode: 500, PromptTokens: 200, CompletionTokens: 100, RealCostUSD: 0.02, BilledCostUSD: 0.03, CreatedAt: now.Add(time.Second)})

	stats, err := s.LogStats()
	if err != nil {
		t.Fatalf("LogStats: %v", err)
	}
	if stats.Total != 2 {
		t.Fatalf("Total: want 2, got %d", stats.Total)
	}
	if stats.Errors != 1 {
		t.Fatalf("Errors: want 1, got %d", stats.Errors)
	}
	if stats.PromptTokens != 300 {
		t.Fatalf("PromptTokens: want 300, got %d", stats.PromptTokens)
	}
	if stats.CompletionTokens != 150 {
		t.Fatalf("CompletionTokens: want 150, got %d", stats.CompletionTokens)
	}
}

func TestLogStatsEmpty(t *testing.T) {
	s := openTempWithLogs(t)
	stats, err := s.LogStats()
	if err != nil {
		t.Fatalf("LogStats: %v", err)
	}
	if stats.Total != 0 {
		t.Fatalf("expected 0, got %d", stats.Total)
	}
}

// ---------- QueryLogs ----------

func TestQueryLogs(t *testing.T) {
	s := openTempWithLogs(t)
	now := time.Now()
	_ = s.CreateLog(&model.Log{TokenID: 1, ChannelID: 1, Model: "gpt-4", StatusCode: 200, CreatedAt: now})
	_ = s.CreateLog(&model.Log{TokenID: 1, ChannelID: 1, Model: "gpt-3.5", StatusCode: 200, CreatedAt: now.Add(time.Second)})
	_ = s.CreateLog(&model.Log{TokenID: 2, ChannelID: 2, Model: "gpt-4", StatusCode: 200, CreatedAt: now.Add(2 * time.Second)})

	logs, total, err := s.QueryLogs(LogFilter{Model: "gpt-4"})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected total=2, got %d", total)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(logs))
	}
}

func TestQueryLogsPagination(t *testing.T) {
	s := openTempWithLogs(t)
	now := time.Now()
	for i := 0; i < 20; i++ {
		_ = s.CreateLog(&model.Log{Model: "m", StatusCode: 200, CreatedAt: now.Add(time.Duration(i) * time.Second)})
	}

	logs1, total, err := s.QueryLogs(LogFilter{Limit: 5, Offset: 0})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if total != 20 {
		t.Fatalf("expected total=20, got %d", total)
	}
	if len(logs1) != 5 {
		t.Fatalf("page1: expected 5, got %d", len(logs1))
	}

	logs2, _, err := s.QueryLogs(LogFilter{Limit: 5, Offset: 5})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(logs2) != 5 {
		t.Fatalf("page2: expected 5, got %d", len(logs2))
	}
}

// ---------- TimeSeries ----------

func TestTimeSeries(t *testing.T) {
	s := openTempWithLogs(t)
	base := time.Now().Truncate(time.Hour)
	_ = s.CreateLog(&model.Log{Model: "m", StatusCode: 200, CreatedAt: base})
	_ = s.CreateLog(&model.Log{Model: "m", StatusCode: 500, CreatedAt: base.Add(time.Minute)})
	_ = s.CreateLog(&model.Log{Model: "m", StatusCode: 200, CreatedAt: base.Add(2 * time.Hour)})

	buckets, err := s.TimeSeries(LogFilter{}, 3600)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(buckets))
	}
	if buckets[0].Requests != 2 {
		t.Fatalf("bucket[0].Requests: want 2, got %d", buckets[0].Requests)
	}
	if buckets[0].Errors != 1 {
		t.Fatalf("bucket[0].Errors: want 1, got %d", buckets[0].Errors)
	}
}

// ---------- TopBy* ----------

func TestTopByModel(t *testing.T) {
	s := openTempWithLogs(t)
	now := time.Now()
	_ = s.CreateLog(&model.Log{Model: "gpt-4", StatusCode: 200, PromptTokens: 100, CompletionTokens: 50, BilledCostUSD: 0.01, CreatedAt: now})
	_ = s.CreateLog(&model.Log{Model: "gpt-4", StatusCode: 200, PromptTokens: 100, CompletionTokens: 50, BilledCostUSD: 0.01, CreatedAt: now.Add(time.Second)})
	_ = s.CreateLog(&model.Log{Model: "claude", StatusCode: 200, PromptTokens: 200, CompletionTokens: 100, BilledCostUSD: 0.02, CreatedAt: now.Add(2 * time.Second)})

	results, err := s.TopByModel(LogFilter{}, 10)
	if err != nil {
		t.Fatalf("TopByModel: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 models, got %d", len(results))
	}
	if results[0].Label != "gpt-4" {
		t.Fatalf("expected gpt-4 first, got %s", results[0].Label)
	}
}

func TestTopByChannel(t *testing.T) {
	s := openTempWithLogs(t)
	now := time.Now()
	_ = s.CreateLog(&model.Log{ChannelID: 10, Model: "m", StatusCode: 200, CreatedAt: now})
	_ = s.CreateLog(&model.Log{ChannelID: 10, Model: "m", StatusCode: 200, CreatedAt: now.Add(time.Second)})
	_ = s.CreateLog(&model.Log{ChannelID: 20, Model: "m", StatusCode: 200, CreatedAt: now.Add(2 * time.Second)})

	results, err := s.TopByChannel(LogFilter{}, 10)
	if err != nil {
		t.Fatalf("TopByChannel: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(results))
	}
}

func TestTopByToken(t *testing.T) {
	s := openTempWithLogs(t)
	now := time.Now()
	_ = s.CreateLog(&model.Log{TokenID: 100, Model: "m", StatusCode: 200, CreatedAt: now})
	_ = s.CreateLog(&model.Log{TokenID: 200, Model: "m", StatusCode: 200, CreatedAt: now.Add(time.Second)})

	results, err := s.TopByToken(LogFilter{}, 10)
	if err != nil {
		t.Fatalf("TopByToken: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(results))
	}
}
