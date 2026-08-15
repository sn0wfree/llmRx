package store

import (
	"context"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestModelCostCRUD(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	// Empty initially.
	if n, err := s.CountModelCosts(); err != nil || n != 0 {
		t.Fatalf("expected empty, got %d err=%v", n, err)
	}

	c := &model.ModelCost{
		Model:                    "deepseek-chat",
		Provider:                 "deepseek",
		InputPricePer1M:          0.14,
		OutputPricePer1M:         0.28,
		CacheReadPricePer1M:      0.014,
		ReasoningPricePer1M:      0.5,
		ImagePrice:               0.02,
		InputPricePer1MAbove128K: 0.1,
	}
	if err := s.UpsertModelCost(c); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.GetModelCost("deepseek-chat")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.InputPricePer1M != 0.14 || got.CacheReadPricePer1M != 0.014 {
		t.Errorf("unexpected cost: %+v", got)
	}

	// Upsert overwrites.
	c.InputPricePer1M = 0.20
	if err := s.UpsertModelCost(c); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, _ = s.GetModelCost("deepseek-chat")
	if got.InputPricePer1M != 0.20 {
		t.Errorf("expected updated price 0.20, got %f", got.InputPricePer1M)
	}

	if err := s.DeleteModelCost("deepseek-chat"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetModelCost("deepseek-chat"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestModelCostBatchAndList(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	costs := []model.ModelCost{
		{Model: "b-model", InputPricePer1M: 1, OutputPricePer1M: 2},
		{Model: "a-model", InputPricePer1M: 3, OutputPricePer1M: 4},
	}
	if err := s.BatchUpsertModelCosts(costs); err != nil {
		t.Fatalf("batch: %v", err)
	}
	// Re-running must be idempotent (upsert, no duplicate).
	if err := s.BatchUpsertModelCosts(costs); err != nil {
		t.Fatalf("batch rerun: %v", err)
	}
	if n, _ := s.CountModelCosts(); n != 2 {
		t.Fatalf("expected 2 rows after idempotent batch, got %d", n)
	}
	rows, err := s.ListModelCosts()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 || rows[0].Model != "a-model" {
		t.Errorf("expected sorted list, got %+v", rows)
	}
}

func TestBackfillTokenUserIDs(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	u1 := &model.User{Username: "alice", PasswordHash: "h1", Role: model.RoleUser, Status: 1}
	u2 := &model.User{Username: "bob", PasswordHash: "h2", Role: model.RoleUser, Status: 1}
	if err := s.CreateUser(u1); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(u2); err != nil {
		t.Fatal(err)
	}

	// Registration-flow tokens named after the user.
	t1 := &model.Token{Key: "sk-1", Name: "alice", Status: 0}
	t2 := &model.Token{Key: "sk-2", Name: "bob", Status: 0}
	t3 := &model.Token{Key: "sk-3", Name: "unlinked", Status: 0} // no matching user
	if err := s.CreateToken(t1); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateToken(t2); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateToken(t3); err != nil {
		t.Fatal(err)
	}

	n, err := s.BackfillTokenUserIDs(context.Background())
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 tokens backfilled, got %d", n)
	}

	toks, err := s.GetTokensByUserID(u1.ID)
	if err != nil || len(toks) != 1 || toks[0].Name != "alice" {
		t.Fatalf("alice tokens: %+v err=%v", toks, err)
	}
	if len(toks) == 1 && toks[0].UserID != u1.ID {
		t.Errorf("expected user_id %d, got %d", u1.ID, toks[0].UserID)
	}

	// Idempotent second run.
	if n, _ := s.BackfillTokenUserIDs(context.Background()); n != 0 {
		t.Errorf("second backfill should be a no-op, updated %d", n)
	}
}
