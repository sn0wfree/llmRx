package store

import (
	"errors"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestPlans_CRUD(t *testing.T) {
	s := openTemp(t)
	p := &model.Plan{Name: "starter", BudgetUSD: 10.0, MarkupRatio: 1.2, Status: 1}
	if err := s.CreatePlan(p); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if p.ID == 0 {
		t.Fatal("CreatePlan did not set ID")
	}
	got, err := s.GetPlan(p.ID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Name != "starter" || got.BudgetUSD != 10.0 || got.MarkupRatio != 1.2 {
		t.Fatalf("GetPlan mismatch: %+v", got)
	}
	got.Name = "pro"
	got.BudgetUSD = 50.0
	got.MarkupRatio = 1.5
	if err := s.UpdatePlan(got); err != nil {
		t.Fatalf("UpdatePlan: %v", err)
	}
	updated, _ := s.GetPlan(p.ID)
	if updated.Name != "pro" || updated.BudgetUSD != 50.0 || updated.MarkupRatio != 1.5 {
		t.Fatalf("UpdatePlan not persisted: %+v", updated)
	}
	all, err := s.GetPlans()
	if err != nil {
		t.Fatalf("GetPlans: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("GetPlans: expected 1, got %d", len(all))
	}
	if err := s.DeletePlan(p.ID); err != nil {
		t.Fatalf("DeletePlan: %v", err)
	}
	if _, err := s.GetPlan(p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestPlans_GetMultiple(t *testing.T) {
	s := openTemp(t)
	for _, name := range []string{"free", "basic", "pro"} {
		if err := s.CreatePlan(&model.Plan{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.GetPlans()
	if err != nil {
		t.Fatalf("GetPlans: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 plans, got %d", len(all))
	}
	if all[0].Name != "free" || all[2].Name != "pro" {
		t.Fatalf("plans not ordered by id: %s, %s", all[0].Name, all[2].Name)
	}
}

func TestPlans_GetNotFound(t *testing.T) {
	s := openTemp(t)
	_, err := s.GetPlan(999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPlans_DeleteNonexistent(t *testing.T) {
	s := openTemp(t)
	if err := s.DeletePlan(999); err != nil {
		t.Fatalf("deleting non-existent plan should not error, got %v", err)
	}
}

func TestPlans_UpdateSetsUpdatedAt(t *testing.T) {
	s := openTemp(t)
	p := &model.Plan{Name: "x", MarkupRatio: 1.0}
	if err := s.CreatePlan(p); err != nil {
		t.Fatal(err)
	}
	original := p.UpdatedAt
	time.Sleep(1100 * time.Millisecond)
	p.MarkupRatio = 2.0
	if err := s.UpdatePlan(p); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetPlan(p.ID)
	if !got.UpdatedAt.After(original) {
		t.Fatalf("UpdatedAt not advanced: was %v, now %v", original, got.UpdatedAt)
	}
}

func TestUsers_CRUD(t *testing.T) {
	s := openTemp(t)
	u := &model.User{Username: "alice", PasswordHash: "hash1", Role: model.RoleAdmin, Status: 1}
	if err := s.CreateUser(u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID == 0 {
		t.Fatal("CreateUser did not set ID")
	}
	got, err := s.GetUser(u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Username != "alice" || got.Role != model.RoleAdmin {
		t.Fatalf("GetUser mismatch: %+v", got)
	}
	got.Role = model.RoleRoot
	got.Status = 0
	got.SessionToken = "sess-xyz"
	if err := s.UpdateUser(got); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	updated, _ := s.GetUser(u.ID)
	if updated.Role != model.RoleRoot || updated.Status != 0 || updated.SessionToken != "sess-xyz" {
		t.Fatalf("UpdateUser not persisted: %+v", updated)
	}
	all, err := s.GetUsers()
	if err != nil {
		t.Fatalf("GetUsers: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("GetUsers: expected 1, got %d", len(all))
	}
}

func TestUsers_GetNotFound(t *testing.T) {
	s := openTemp(t)
	_, err := s.GetUser(999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	_, err = s.GetUserByUsername("nobody")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUsers_GetBySessionWithExpiry(t *testing.T) {
	s := openTemp(t)
	future := time.Now().Add(1 * time.Hour)
	u := &model.User{
		Username: "sess-user", PasswordHash: "h", Role: model.RoleUser,
		Status: 1, SessionToken: "tok-active", SessionExp: &future,
	}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetUserBySession("tok-active")
	if err != nil || got == nil || got.ID != u.ID {
		t.Fatalf("GetUserBySession active: %v %v", got, err)
	}
	expired := time.Now().Add(-1 * time.Hour)
	u.SessionExp = &expired
	if err := s.UpdateUser(u); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetUserBySession("tok-active"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session should not match, got %v", err)
	}
}

func TestUsers_GetBySessionDisabledUser(t *testing.T) {
	s := openTemp(t)
	future := time.Now().Add(1 * time.Hour)
	u := &model.User{
		Username: "disabled", PasswordHash: "h", Role: model.RoleUser,
		Status: 0, SessionToken: "tok-disabled", SessionExp: &future,
	}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetUserBySession("tok-disabled"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled user session should not match, got %v", err)
	}
}

func TestUsers_CleanupExpiredSessions(t *testing.T) {
	s := openTemp(t)
	past := time.Now().Add(-1 * time.Hour)
	future := time.Now().Add(1 * time.Hour)
	u1 := &model.User{Username: "expired", PasswordHash: "h", Role: model.RoleUser, Status: 1, SessionToken: "t1", SessionExp: &past}
	u2 := &model.User{Username: "active", PasswordHash: "h", Role: model.RoleUser, Status: 1, SessionToken: "t2", SessionExp: &future}
	u3 := &model.User{Username: "noinit", PasswordHash: "h", Role: model.RoleUser, Status: 1, SessionToken: "t3"}
	for _, u := range []*model.User{u1, u2, u3} {
		if err := s.CreateUser(u); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.CleanupExpiredSessions()
	if err != nil {
		t.Fatalf("CleanupExpiredSessions: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 expired session cleared, got %d", n)
	}
	cleared, _ := s.GetUser(u1.ID)
	if cleared.SessionToken != "" {
		t.Fatalf("expired session token should be cleared, got %q", cleared.SessionToken)
	}
	stillActive, _ := s.GetUser(u2.ID)
	if stillActive.SessionToken != "t2" {
		t.Fatalf("active session token should be intact, got %q", stillActive.SessionToken)
	}
}

func TestUsers_UpdatePasswordAndRole(t *testing.T) {
	s := openTemp(t)
	u := &model.User{Username: "pwuser", PasswordHash: "old", Role: model.RoleUser, Status: 1}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	u.PasswordHash = "newhash"
	u.Role = model.RoleRoot
	if err := s.UpdateUser(u); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetUser(u.ID)
	if got.PasswordHash != "newhash" || got.Role != model.RoleRoot {
		t.Fatalf("password/role not updated: %+v", got)
	}
}
