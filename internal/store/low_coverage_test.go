package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

func openTempLog(t *testing.T) (*store.SQLite, *logstore.Manager) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.OpenSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	logDir := filepath.Join(dir, "logs")
	if err := logstore.EnsureDir(logDir); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	ls, err := logstore.New(logDir, nil)
	if err != nil {
		t.Fatalf("logstore.New: %v", err)
	}
	t.Cleanup(func() { _ = ls.Close() })
	s.SetLogStore(ls)
	return s, ls
}

func TestUpdateToken_NotFound(t *testing.T) {
	s, _ := openTempLog(t)
	err := s.UpdateToken(&model.Token{ID: 9999, Key: "sk-x"})
	if err != store.ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestIncrementTokenSpend_ZeroAmount(t *testing.T) {
	s, _ := openTempLog(t)
	tok := &model.Token{Key: "sk-t", Status: model.TokenActive}
	s.CreateToken(tok)
	if err := s.IncrementTokenSpend(tok.ID, 0); err != nil {
		t.Errorf("zero amount should be no-op: %v", err)
	}
}

func TestIncrementTokenSpend_NotFound(t *testing.T) {
	s, _ := openTempLog(t)
	err := s.IncrementTokenSpend(9999, 1.0)
	if err != store.ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestRecordRequestSpend_ZeroAmount(t *testing.T) {
	s, _ := openTempLog(t)
	if err := s.RecordRequestSpend(1, 1, 0); err != nil {
		t.Errorf("zero amount should be no-op: %v", err)
	}
}

func TestRecordRequestSpend_ZeroTokenID(t *testing.T) {
	s, _ := openTempLog(t)
	if err := s.RecordRequestSpend(0, 1, 1.0); err != nil {
		t.Errorf("zero tokenID should be no-op: %v", err)
	}
}

func TestLogStats_WithLogs(t *testing.T) {
	s, _ := openTempLog(t)
	s.CreateLog(&model.Log{
		Model: "gpt-4", PromptTokens: 100, CompletionTokens: 50,
		RealCostUSD: 1.5, BilledCostUSD: 2.0, StatusCode: 200,
	})
	s.CreateLog(&model.Log{
		Model: "gpt-4", PromptTokens: 200, CompletionTokens: 100,
		RealCostUSD: 3.0, BilledCostUSD: 4.0, StatusCode: 500,
	})
	stats, err := s.LogStats()
	if err != nil {
		t.Fatalf("LogStats: %v", err)
	}
	if stats.Total != 2 {
		t.Errorf("total=%d want 2", stats.Total)
	}
	if stats.Errors != 1 {
		t.Errorf("errors=%d want 1", stats.Errors)
	}
	if stats.PromptTokens != 300 {
		t.Errorf("prompt=%d want 300", stats.PromptTokens)
	}
	if stats.CompletionTokens != 150 {
		t.Errorf("completion=%d want 150", stats.CompletionTokens)
	}
}

func TestLogStats_NoLogStore(t *testing.T) {
	dir := t.TempDir()
	s, err := store.OpenSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	stats, err := s.LogStats()
	if err != nil {
		t.Fatalf("LogStats: %v", err)
	}
	if stats.Total != 0 {
		t.Errorf("total=%d want 0", stats.Total)
	}
}

func TestQueryLogs_WithFilters(t *testing.T) {
	s, _ := openTempLog(t)
	s.CreateLog(&model.Log{Model: "gpt-4", PromptTokens: 10, StatusCode: 200})
	s.CreateLog(&model.Log{Model: "claude-3", PromptTokens: 20, StatusCode: 500})

	logs, total, err := s.QueryLogs(store.LogFilter{Limit: 100})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if total != 2 {
		t.Errorf("total=%d want 2", total)
	}
	if len(logs) != 2 {
		t.Errorf("len=%d want 2", len(logs))
	}
}

func TestQueryLogs_NoLogStore(t *testing.T) {
	dir := t.TempDir()
	s, err := store.OpenSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	logs, total, err := s.QueryLogs(store.LogFilter{Limit: 100})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if total != 0 || len(logs) != 0 {
		t.Errorf("expected empty results")
	}
}

func TestGetLogs_NoLogStore(t *testing.T) {
	dir := t.TempDir()
	s, err := store.OpenSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	logs, err := s.GetLogs(10, 0)
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if len(logs) != 0 {
		t.Errorf("expected empty")
	}
}

func TestCountLogs_NoLogStore(t *testing.T) {
	dir := t.TempDir()
	s, err := store.OpenSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	count, err := s.CountLogs()
	if err != nil {
		t.Fatalf("CountLogs: %v", err)
	}
	if count != 0 {
		t.Errorf("count=%d want 0", count)
	}
}

func TestCountLogs_WithLogs(t *testing.T) {
	s, _ := openTempLog(t)
	s.CreateLog(&model.Log{Model: "m", StatusCode: 200})
	s.CreateLog(&model.Log{Model: "m", StatusCode: 200})
	count, err := s.CountLogs()
	if err != nil {
		t.Fatalf("CountLogs: %v", err)
	}
	if count != 2 {
		t.Errorf("count=%d want 2", count)
	}
}

func TestTimeSeries_NoLogStore(t *testing.T) {
	dir := t.TempDir()
	s, err := store.OpenSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	pts, err := s.TimeSeries(store.LogFilter{Limit: 100}, 3600)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if len(pts) != 0 {
		t.Errorf("expected empty")
	}
}

func TestTimeSeries_WithLogs(t *testing.T) {
	s, _ := openTempLog(t)
	s.CreateLog(&model.Log{Model: "m", PromptTokens: 10, StatusCode: 200})
	pts, err := s.TimeSeries(store.LogFilter{Limit: 100}, 86400)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if len(pts) == 0 {
		t.Errorf("expected at least 1 bucket")
	}
}

func TestDeleteLogsBefore_NoLogStore(t *testing.T) {
	dir := t.TempDir()
	s, err := store.OpenSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	n, err := s.DeleteLogsBefore(time.Now().Unix())
	if err != nil {
		t.Fatalf("DeleteLogsBefore: %v", err)
	}
	if n != 0 {
		t.Errorf("n=%d want 0", n)
	}
}

func TestUpdateChannel_NoErrorOnMissingID(t *testing.T) {
	s, _ := openTempLog(t)
	ch := &model.Channel{ID: 9999, Name: "x", Provider: "openai", Protocol: "openai", BaseURL: "https://x", Models: []string{"m"}}
	if err := s.UpdateChannel(ch); err != nil {
		t.Errorf("UpdateChannel on missing ID should not error: %v", err)
	}
}

func TestGetChannels_Empty(t *testing.T) {
	s, _ := openTempLog(t)
	chs, err := s.GetChannels()
	if err != nil {
		t.Fatalf("GetChannels: %v", err)
	}
	if len(chs) != 0 {
		t.Errorf("expected empty, got %d", len(chs))
	}
}

func TestUpdateAlert_NoErrorOnMissingID(t *testing.T) {
	s, _ := openTempLog(t)
	a := &model.Alert{ID: 9999, Name: "x", Type: "cost", Threshold: 1, WindowSec: 300, CooldownSec: 300}
	if err := s.UpdateAlert(a); err != nil {
		t.Errorf("UpdateAlert on missing ID should not error: %v", err)
	}
}

func TestCreateAlertEvent_WithAlert(t *testing.T) {
	s, _ := openTempLog(t)
	a := &model.Alert{Name: "a", Type: "cost", Threshold: 1, WindowSec: 300, CooldownSec: 300, Enabled: true}
	s.CreateAlert(a)
	e := &model.AlertEvent{AlertID: a.ID, AlertName: "a", FiredAt: time.Now()}
	if err := s.CreateAlertEvent(e); err != nil {
		t.Fatalf("CreateAlertEvent: %v", err)
	}
}
