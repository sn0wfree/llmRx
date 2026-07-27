package store_test

import (
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

// --- RecordRequestSpend edge cases ---

func TestRecordRequestSpend_NonExistentPlan(t *testing.T) {
	s, _ := openTempLog(t)
	tok := &model.Token{Key: "sk-t", Name: "t1", Status: model.TokenActive}
	s.CreateToken(tok)

	err := s.RecordRequestSpend(tok.ID, 9999, 0.5)
	if err != store.ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestRecordRequestSpend_NegativeAmount(t *testing.T) {
	s, _ := openTempLog(t)
	tok := &model.Token{Key: "sk-t", Name: "t1", Status: model.TokenActive}
	s.CreateToken(tok)

	// First, add some spend
	s.RecordRequestSpend(tok.ID, 0, 10.0)
	t1, _ := s.GetTokenByID(tok.ID)
	if t1.UsedUSD != 10.0 {
		t.Fatalf("expected 10.0, got %f", t1.UsedUSD)
	}

	// Now credit back
	err := s.RecordRequestSpend(tok.ID, 0, -5.0)
	if err != nil {
		t.Fatalf("negative amount: %v", err)
	}
	t2, _ := s.GetTokenByID(tok.ID)
	if t2.UsedUSD != 5.0 {
		t.Errorf("expected 5.0 after credit, got %f", t2.UsedUSD)
	}
}

// --- OpenSQLite edge cases ---

func TestOpenSQLite_EmptyDSN(t *testing.T) {
	_, err := store.OpenSQLite("")
	if err == nil {
		t.Errorf("expected error for empty DSN")
	}
}

// --- CreateKey/CreateToken edge cases ---

func TestCreateKey_EmptyKey(t *testing.T) {
	s, _ := openTempLog(t)
	err := s.CreateKey(&model.Key{ChannelID: 1, Key: "", KeyMasked: "", Status: model.KeyActive})
	if err == nil {
		t.Errorf("expected error for empty key")
	}
}

func TestCreateToken_EmptyKey(t *testing.T) {
	s, _ := openTempLog(t)
	err := s.CreateToken(&model.Token{Key: "", Name: "t", Status: model.TokenActive})
	if err == nil {
		t.Errorf("expected error for empty token key")
	}
}

// --- CreateAlertEvent edge cases ---

func TestCreateAlertEvent_ZeroFiredAt(t *testing.T) {
	s, _ := openTempLog(t)
	a := &model.Alert{Name: "a", Type: "cost_spike", Threshold: 10, WindowSec: 300, CooldownSec: 300, Enabled: true}
	s.CreateAlert(a)
	e := &model.AlertEvent{AlertID: a.ID, AlertName: "a", FiredAt: time.Time{}}
	if err := s.CreateAlertEvent(e); err != nil {
		t.Fatalf("CreateAlertEvent: %v", err)
	}
	if e.FiredAt.IsZero() {
		t.Errorf("FiredAt should be auto-set to now")
	}
}

// --- GetAlertEvents edge cases ---

func TestGetAlertEvents_NegativeLimit(t *testing.T) {
	s, _ := openTempLog(t)
	events, err := s.GetAlertEvents(-1)
	if err != nil {
		t.Fatalf("GetAlertEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected empty, got %d", len(events))
	}
}

// --- IncrementPlanSpend edge cases ---

func TestIncrementPlanSpend_NegativeAmount(t *testing.T) {
	s, _ := openTempLog(t)
	p := &model.Plan{Name: "p", MarkupRatio: 1.0, BudgetUSD: 100, Status: 1}
	s.CreatePlan(p)

	s.IncrementPlanSpend(p.ID, 10.0)
	plan, _ := s.GetPlan(p.ID)
	if plan.UsedUSD != 10.0 {
		t.Fatalf("expected 10.0, got %f", plan.UsedUSD)
	}

	err := s.IncrementPlanSpend(p.ID, -5.0)
	if err != nil {
		t.Fatalf("negative amount: %v", err)
	}
	plan2, _ := s.GetPlan(p.ID)
	if plan2.UsedUSD != 5.0 {
		t.Errorf("expected 5.0 after credit, got %f", plan2.UsedUSD)
	}
}

// --- CleanupExpiredSessions edge cases ---

func TestCleanupExpiredSessions_NoExpired(t *testing.T) {
	s, _ := openTempLog(t)
	hash, _ := authHashForTest("pw123")
	u := &model.User{Username: "u1", PasswordHash: hash, Role: model.RoleUser, Status: 1}
	future := time.Now().Add(24 * time.Hour)
	u.SessionToken = "tok"
	u.SessionExp = &future
	s.CreateUser(u)

	n, err := s.CleanupExpiredSessions()
	if err != nil {
		t.Fatalf("CleanupExpiredSessions: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0, got %d", n)
	}
}

func TestCleanupExpiredSessions_NoExpirySet(t *testing.T) {
	s, _ := openTempLog(t)
	hash, _ := authHashForTest("pw123")
	u := &model.User{Username: "u1", PasswordHash: hash, Role: model.RoleUser, Status: 1, SessionToken: "tok"}
	s.CreateUser(u)

	n, err := s.CleanupExpiredSessions()
	if err != nil {
		t.Fatalf("CleanupExpiredSessions: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0, got %d", n)
	}
}

// --- UpdateChannel protocol default ---

func TestUpdateChannel_EmptyProtocolDefaults(t *testing.T) {
	s, _ := openTempLog(t)
	ch := &model.Channel{Name: "ch", Provider: "openai", Protocol: "openai", BaseURL: "https://x", Models: []string{"m"}, Status: model.ChannelEnabled}
	s.CreateChannel(ch)

	ch.Protocol = ""
	s.UpdateChannel(ch)

	got, _ := s.GetChannel(ch.ID)
	if got.Protocol != "openai" {
		t.Errorf("protocol=%q, want openai", got.Protocol)
	}
}

// --- DisableAlert edge cases ---

func TestDisableAlert_EmptyReason(t *testing.T) {
	s, _ := openTempLog(t)
	a := &model.Alert{Name: "a", Type: "cost_spike", Threshold: 10, Enabled: true}
	s.CreateAlert(a)

	if err := s.DisableAlert(a.ID, ""); err != nil {
		t.Fatalf("DisableAlert: %v", err)
	}
	got, _ := s.GetAlert(a.ID)
	if got.Enabled {
		t.Errorf("should be disabled")
	}
}

// helper for auth hash in store tests
func authHashForTest(pw string) (string, error) {
	// Use a simple hash for testing - import would create cycle
	return "argon2id$v=19$m=65536,t=3,p=1$" + pw, nil
}
