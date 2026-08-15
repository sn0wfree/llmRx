package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

func newTestStore(t *testing.T) Store {
	t.Helper()
	s, err := Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return s
}

func TestRedeemCodeCRUD(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	c := &model.RedeemCode{
		Code: "RXM-TEST0001", Kind: model.RedeemKindInvite,
		InitialQuota: 5, Role: model.RoleUser, CreatedBy: 1,
	}
	if err := s.CreateRedeemCode(c); err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.ID == 0 {
		t.Fatal("expected generated id")
	}

	got, err := s.GetRedeemCodeByCode("RXM-TEST0001")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Kind != model.RedeemKindInvite || got.InitialQuota != 5 {
		t.Errorf("got %+v", got)
	}

	if _, err := s.GetRedeemCodeByCode("NOPE"); !errors.Is(err, ErrRedeemCodeNotFound) {
		t.Errorf("expected not found, got %v", err)
	}

	codes, err := s.ListRedeemCodes()
	if err != nil || len(codes) != 1 {
		t.Errorf("list: %v n=%d", err, len(codes))
	}
}

func TestUseInviteCode(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	if err := s.CreateRedeemCode(&model.RedeemCode{
		Code: "RXM-INVITE1", Kind: model.RedeemKindInvite,
		InitialQuota: 10, Role: model.RoleUser, CreatedBy: 1,
	}); err != nil {
		t.Fatal(err)
	}

	res, err := s.UseInviteCode(context.Background(), "RXM-INVITE1", "alice", "hash1")
	if err != nil {
		t.Fatalf("use invite: %v", err)
	}
	if res.User.Username != "alice" || res.User.Role != model.RoleUser {
		t.Errorf("user: %+v", res.User)
	}
	if res.Token.Key == "" || res.Token.PlanID != res.Plan.ID {
		t.Errorf("token: %+v", res.Token)
	}
	if res.Plan.BudgetUSD != 10 {
		t.Errorf("plan budget: %f", res.Plan.BudgetUSD)
	}

	// Reuse must fail.
	if _, err := s.UseInviteCode(context.Background(), "RXM-INVITE1", "bob", "hash2"); !errors.Is(err, ErrRedeemCodeUsed) {
		t.Errorf("expected used, got %v", err)
	}

	// Username collision must fail.
	if err := s.CreateRedeemCode(&model.RedeemCode{
		Code: "RXM-INVITE2", Kind: model.RedeemKindInvite,
		InitialQuota: 5, Role: model.RoleUser, CreatedBy: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UseInviteCode(context.Background(), "RXM-INVITE2", "alice", "hash3"); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("expected username taken, got %v", err)
	}

	// Wrong kind must fail (topup code used as invite).
	if err := s.CreateRedeemCode(&model.RedeemCode{
		Code: "RXM-TOPUP1", Kind: model.RedeemKindTopup, AmountUSD: 20, CreatedBy: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UseInviteCode(context.Background(), "RXM-TOPUP1", "carol", "hash4"); !errors.Is(err, ErrRedeemWrongKind) {
		t.Errorf("expected wrong kind, got %v", err)
	}
}

func TestRedeemTopup(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	// Register alice first to get a plan.
	if err := s.CreateRedeemCode(&model.RedeemCode{
		Code: "RXM-INVITE1", Kind: model.RedeemKindInvite,
		InitialQuota: 5, Role: model.RoleUser, CreatedBy: 1,
	}); err != nil {
		t.Fatal(err)
	}
	res, err := s.UseInviteCode(context.Background(), "RXM-INVITE1", "alice", "hash1")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.CreateRedeemCode(&model.RedeemCode{
		Code: "RXM-TOPUP1", Kind: model.RedeemKindTopup, AmountUSD: 20, CreatedBy: 1,
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := s.RedeemTopup(context.Background(), "RXM-TOPUP1", res.Plan.ID)
	if err != nil {
		t.Fatalf("redeem topup: %v", err)
	}
	if plan.BudgetUSD != 25 {
		t.Errorf("budget: %f, want 25", plan.BudgetUSD)
	}

	// Reuse must fail.
	if _, err := s.RedeemTopup(context.Background(), "RXM-TOPUP1", res.Plan.ID); !errors.Is(err, ErrRedeemCodeUsed) {
		t.Errorf("expected used, got %v", err)
	}

	// Wrong kind must fail (invite code as topup).
	if _, err := s.RedeemTopup(context.Background(), "RXM-INVITE1", res.Plan.ID); !errors.Is(err, ErrRedeemWrongKind) {
		t.Errorf("expected wrong kind, got %v", err)
	}
}

func TestRedeemCodeExpired(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	if err := s.CreateRedeemCode(&model.RedeemCode{
		Code: "RXM-EXPIRED1", Kind: model.RedeemKindInvite,
		InitialQuota: 5, Role: model.RoleUser, CreatedBy: 1,
		ExpiresAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UseInviteCode(context.Background(), "RXM-EXPIRED1", "dave", "hash1"); !errors.Is(err, ErrRedeemCodeExpired) {
		t.Errorf("expected expired, got %v", err)
	}
}

func TestRedeemTopupExpired(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	// Register alice for a plan.
	if err := s.CreateRedeemCode(&model.RedeemCode{
		Code: "RXM-INVITE1", Kind: model.RedeemKindInvite,
		InitialQuota: 5, Role: model.RoleUser, CreatedBy: 1,
	}); err != nil {
		t.Fatal(err)
	}
	res, err := s.UseInviteCode(context.Background(), "RXM-INVITE1", "alice", "hash1")
	if err != nil {
		t.Fatal(err)
	}

	// Expired top-up code must be rejected and leave budget intact.
	if err := s.CreateRedeemCode(&model.RedeemCode{
		Code: "RXM-TOPEXP1", Kind: model.RedeemKindTopup,
		AmountUSD: 20, CreatedBy: 1,
		ExpiresAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemTopup(context.Background(), "RXM-TOPEXP1", res.Plan.ID); !errors.Is(err, ErrRedeemCodeExpired) {
		t.Errorf("expected expired topup, got %v", err)
	}
	plan, err := s.GetPlan(res.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.BudgetUSD != 5 {
		t.Errorf("budget must stay 5 after expired topup, got %f", plan.BudgetUSD)
	}
}
