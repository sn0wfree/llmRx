package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
	"github.com/sn0wfree/llmRx/internal/store"
)

func TestUpdateToken_WithEncryption(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.OpenSQLite(filepath.Join(dir, "test.db"))
	t.Cleanup(func() { _ = s.Close() })
	logDir := filepath.Join(dir, "logs")
	logstore.EnsureDir(logDir)
	ls, _ := logstore.New(logDir, nil)
	t.Cleanup(func() { _ = ls.Close() })
	s.SetLogStore(ls)

	mgr, _ := secrets.FromHexKey("aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899")
	s.SetSecrets(mgr)

	tok := &model.Token{Key: "sk-secret-key", Name: "t1", Status: model.TokenActive}
	s.CreateToken(tok)

	tok.Name = "updated"
	if err := s.UpdateToken(tok); err != nil {
		t.Fatalf("UpdateToken: %v", err)
	}

	updated, _ := s.GetTokenByID(tok.ID)
	if updated.Name != "updated" {
		t.Errorf("name=%q", updated.Name)
	}
}

func TestIncrementTokenSpend_Success(t *testing.T) {
	s, _ := openTempLog(t)
	tok := &model.Token{Key: "sk-t", Name: "t1", Status: model.TokenActive}
	s.CreateToken(tok)
	if err := s.IncrementTokenSpend(tok.ID, 1.5); err != nil {
		t.Fatalf("IncrementTokenSpend: %v", err)
	}
	updated, _ := s.GetTokenByID(tok.ID)
	if updated.UsedUSD != 1.5 {
		t.Errorf("used=%v want 1.5", updated.UsedUSD)
	}
}

func TestRecordRequestSpend_SuccessWithPlan(t *testing.T) {
	s, _ := openTempLog(t)
	tok := &model.Token{Key: "sk-t", Name: "t1", Status: model.TokenActive}
	s.CreateToken(tok)
	p := &model.Plan{Name: "pro", BudgetUSD: 100, Status: 1}
	s.CreatePlan(p)

	if err := s.RecordRequestSpend(tok.ID, p.ID, 5.0); err != nil {
		t.Fatalf("RecordRequestSpend: %v", err)
	}
	updated, _ := s.GetTokenByID(tok.ID)
	if updated.UsedUSD != 5.0 {
		t.Errorf("token used=%v want 5.0", updated.UsedUSD)
	}
	plan, _ := s.GetPlan(p.ID)
	if plan.UsedUSD != 5.0 {
		t.Errorf("plan used=%v want 5.0", plan.UsedUSD)
	}
}

func TestReencryptAllKeys_WithTokens(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.OpenSQLite(filepath.Join(dir, "test.db"))
	t.Cleanup(func() { _ = s.Close() })
	logDir := filepath.Join(dir, "logs")
	logstore.EnsureDir(logDir)
	ls, _ := logstore.New(logDir, nil)
	t.Cleanup(func() { _ = ls.Close() })
	s.SetLogStore(ls)

	oldMgr, _ := secrets.FromHexKey("aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899")
	s.SetSecrets(oldMgr)

	ch := &model.Channel{Name: "ch", Provider: "openai", Protocol: "openai", BaseURL: "https://x", Models: []string{"m"}, Status: model.ChannelEnabled}
	s.CreateChannel(ch)
	s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "sk-secret-key-12345", KeyMasked: "sk-s***1245", Status: model.KeyActive})

	newMgr, _ := secrets.FromHexKey("99887766554433221100ffeeddccbbaa99887766554433221100ffeeddccbbaa")
	n, err := s.ReencryptAllKeys(oldMgr, newMgr)
	if err != nil {
		t.Fatalf("ReencryptAllKeys: %v", err)
	}
	if n < 1 {
		t.Errorf("expected at least 1 key re-encrypted, got %d", n)
	}
}

func TestGetToken_NotFound(t *testing.T) {
	s, _ := openTempLog(t)
	_, err := s.GetToken("nonexistent")
	if err == nil {
		t.Errorf("expected error for nonexistent token")
	}
}

func TestGetPlan_NotFound(t *testing.T) {
	s, _ := openTempLog(t)
	_, err := s.GetPlan(9999)
	if err == nil {
		t.Errorf("expected error for nonexistent plan")
	}
}

func TestGetAlert_NotFound(t *testing.T) {
	s, _ := openTempLog(t)
	_, err := s.GetAlert(9999)
	if err == nil {
		t.Errorf("expected error for nonexistent alert")
	}
}

func TestGetChannel_NotFound(t *testing.T) {
	s, _ := openTempLog(t)
	_, err := s.GetChannel(9999)
	if err == nil {
		t.Errorf("expected error for nonexistent channel")
	}
}

func TestGetKeys_EmptyChannel(t *testing.T) {
	s, _ := openTempLog(t)
	ch := &model.Channel{Name: "ch", Provider: "openai", Protocol: "openai", BaseURL: "https://x", Models: []string{"m"}, Status: model.ChannelEnabled}
	s.CreateChannel(ch)
	keys, err := s.GetKeys(ch.ID)
	if err != nil {
		t.Fatalf("GetKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected empty keys, got %d", len(keys))
	}
}

func TestDeleteLogsBefore_WithOldLogs(t *testing.T) {
	s, _ := openTempLog(t)
	old := time.Now().AddDate(0, 0, -10)
	s.CreateLog(&model.Log{Model: "m", StatusCode: 200, CreatedAt: old})
	n, err := s.DeleteLogsBefore(time.Now().Unix())
	if err != nil {
		t.Fatalf("DeleteLogsBefore: %v", err)
	}
	if n < 1 {
		t.Errorf("expected at least 1 file deleted, got %d", n)
	}
}

func TestRawQuery(t *testing.T) {
	s, _ := openTempLog(t)
	rows, err := s.RawQuery("SELECT 1 as val")
	if err != nil {
		t.Fatalf("RawQuery: %v", err)
	}
	defer rows.Close()
}

func TestRawQueryRow(t *testing.T) {
	s, _ := openTempLog(t)
	row := s.RawQueryRow("SELECT 1 as val")
	var v int
	if err := row.Scan(&v); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if v != 1 {
		t.Errorf("val=%d want 1", v)
	}
}

func TestDisableAlert_Success(t *testing.T) {
	s, _ := openTempLog(t)
	a := &model.Alert{Name: "a", Type: "cost_spike", Threshold: 10, WindowSec: 300, CooldownSec: 300, Enabled: true}
	s.CreateAlert(a)
	if err := s.DisableAlert(a.ID, "test"); err != nil {
		t.Fatalf("DisableAlert: %v", err)
	}
	updated, _ := s.GetAlert(a.ID)
	if updated.Enabled {
		t.Errorf("alert should be disabled")
	}
}

func TestAckAlertEvent_Success(t *testing.T) {
	s, _ := openTempLog(t)
	a := &model.Alert{Name: "a", Type: "cost_spike", Threshold: 10, WindowSec: 300, CooldownSec: 300, Enabled: true}
	s.CreateAlert(a)
	e := &model.AlertEvent{AlertID: a.ID, AlertName: "a", FiredAt: time.Now()}
	s.CreateAlertEvent(e)
	if err := s.AckAlertEvent(e.ID); err != nil {
		t.Fatalf("AckAlertEvent: %v", err)
	}
}

func TestGetRuntimeSettings_Empty(t *testing.T) {
	s, _ := openTempLog(t)
	raw, err := s.GetRuntimeSettings()
	if err != nil {
		t.Fatalf("GetRuntimeSettings: %v", err)
	}
	if len(raw) > 0 {
		t.Errorf("expected empty settings, got %d bytes", len(raw))
	}
}

func TestSetRuntimeSettings(t *testing.T) {
	s, _ := openTempLog(t)
	if err := s.SetRuntimeSettings([]byte(`{"key":"val"}`)); err != nil {
		t.Fatalf("SetRuntimeSettings: %v", err)
	}
	raw, _ := s.GetRuntimeSettings()
	if len(raw) == 0 {
		t.Errorf("expected non-empty settings after set")
	}
}
