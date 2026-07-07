package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

func openTemp(t *testing.T) *SQLite {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")
	s, err := OpenSQLite(dsn)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestChannels_CRUD(t *testing.T) {
	s := openTemp(t)

	if err := s.CreateChannel(&model.Channel{
		Name: "deepseek", Provider: "deepseek", BaseURL: "https://api.deepseek.com/v1",
		Models: []string{"deepseek-chat"}, Status: model.ChannelEnabled,
	}); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	ch, err := s.GetChannel(1)
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if ch.Name != "deepseek" || len(ch.Models) != 1 || ch.Models[0] != "deepseek-chat" {
		t.Fatalf("GetChannel: got %+v", ch)
	}

	ch.Status = model.ChannelDisabled
	if err := s.UpdateChannel(ch); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	ch2, _ := s.GetChannel(1)
	if ch2.Status != model.ChannelDisabled {
		t.Fatalf("UpdateChannel: status not updated, got %d", ch2.Status)
	}

	all, err := s.GetChannels()
	if err != nil {
		t.Fatalf("GetChannels: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("GetChannels: expected 1, got %d", len(all))
	}

	if err := s.DeleteChannel(1); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}
	if _, err := s.GetChannel(1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestChannel_UniqueName(t *testing.T) {
	s := openTemp(t)
	ch := &model.Channel{Name: "dup", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	dup := &model.Channel{Name: "dup", Provider: "y", BaseURL: "y", Status: model.ChannelEnabled}
	if err := s.CreateChannel(dup); err == nil {
		t.Fatal("expected unique-name error")
	}
}

func TestKeys_CascadeOnChannelDelete(t *testing.T) {
	s := openTemp(t)
	ch := &model.Channel{Name: "c", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "kk", KeyMasked: "kk", Status: model.KeyActive}); err != nil {
		t.Fatal(err)
	}

	ks, err := s.GetKeys(ch.ID)
	if err != nil || len(ks) != 1 {
		t.Fatalf("GetKeys: ks=%v err=%v", ks, err)
	}

	if err := s.DeleteChannel(ch.ID); err != nil {
		t.Fatal(err)
	}
	ks, err = s.GetKeys(ch.ID)
	if err != nil || len(ks) != 0 {
		t.Fatalf("expected 0 keys after cascade, got %d err=%v", len(ks), err)
	}
}

func TestTokens_Lookup(t *testing.T) {
	s := openTemp(t)
	tok := &model.Token{
		Key: "sk-test-123", Name: "n", Status: model.TokenActive,
		RPM: 60, TPM: 1000,
		ModelsWhitelist: []string{"a", "b"},
		IPWhitelist:     []string{"127.0.0.1"},
	}
	if err := s.CreateToken(tok); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetToken("sk-test-123")
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if got.ID != 1 || got.Name != "n" || got.RPM != 60 || got.TPM != 1000 {
		t.Fatalf("GetToken: got %+v", got)
	}
	if len(got.ModelsWhitelist) != 2 || got.ModelsWhitelist[1] != "b" {
		t.Fatalf("ModelsWhitelist round-trip: %v", got.ModelsWhitelist)
	}

	all, err := s.GetTokens()
	if err != nil || len(all) != 1 {
		t.Fatalf("GetTokens: %v %v", all, err)
	}

	if err := s.DeleteToken(1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetToken("sk-test-123"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestTokens_UpdateAndSpend(t *testing.T) {
	s := openTemp(t)
	tok := &model.Token{Key: "sk-x", Name: "n", Status: model.TokenActive}
	if err := s.CreateToken(tok); err != nil {
		t.Fatal(err)
	}
	if err := s.IncrementTokenSpend(tok.ID, 0.05); err != nil {
		t.Fatalf("first increment: %v", err)
	}
	if err := s.IncrementTokenSpend(tok.ID, 0.10); err != nil {
		t.Fatalf("second increment: %v", err)
	}
	got, err := s.GetTokenByID(tok.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UsedUSD < 0.149999 || got.UsedUSD > 0.150001 {
		t.Fatalf("used_usd accumulated: %f", got.UsedUSD)
	}

	// UpdateToken round-trips RPM and whitelist.
	got.RPM = 99
	got.ModelsWhitelist = []string{"foo"}
	if err := s.UpdateToken(got); err != nil {
		t.Fatalf("UpdateToken: %v", err)
	}
	got2, _ := s.GetTokenByID(tok.ID)
	if got2.RPM != 99 {
		t.Errorf("rpm not persisted: %d", got2.RPM)
	}
	if len(got2.ModelsWhitelist) != 1 || got2.ModelsWhitelist[0] != "foo" {
		t.Errorf("whitelist not persisted: %v", got2.ModelsWhitelist)
	}

	// IncrementPlanSpend with plan_id=0 must be a no-op (no row created).
	if err := s.IncrementPlanSpend(0, 1.0); err != nil {
		t.Errorf("plan_id=0 should not error: %v", err)
	}

	// IncrementPlanSpend on a non-existent plan returns ErrNotFound
	// (after we removed the zero-row shortcut).
	if err := s.IncrementPlanSpend(999, 1.0); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestUsers_UniqueUsernameAndSessionLookup(t *testing.T) {
	s := openTemp(t)
	u := &model.User{Username: "admin", PasswordHash: "x", Role: model.RoleRoot, Status: 1, SessionToken: "abc"}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	dup := &model.User{Username: "admin", PasswordHash: "y", Role: model.RoleUser}
	if err := s.CreateUser(dup); err == nil {
		t.Fatal("expected unique username violation")
	}

	got, err := s.GetUserByUsername("admin")
	if err != nil || got == nil || got.ID != 1 {
		t.Fatalf("GetUserByUsername: %v %v", got, err)
	}

	if u2, _ := s.GetUserBySession("abc"); u2 == nil || u2.ID != 1 {
		t.Fatalf("GetUserBySession: got %+v", u2)
	}
	if u3, _ := s.GetUserBySession(""); u3 != nil {
		t.Fatalf("empty session should not match, got %+v", u3)
	}
}

func TestLogs_Aggregations(t *testing.T) {
	s := openTemp(t)

	now := time.Now()
	rows := []*model.Log{
		{TokenID: 1, ChannelID: 1, Model: "deepseek-chat", PromptTokens: 100, CompletionTokens: 50, RealCostUSD: 0.01, BilledCostUSD: 0.01, DurationMs: 100, StatusCode: 200, RouterPath: "L1→L2", CreatedAt: now},
		{TokenID: 1, ChannelID: 1, Model: "deepseek-chat", PromptTokens: 200, CompletionTokens: 100, RealCostUSD: 0.02, BilledCostUSD: 0.02, DurationMs: 200, StatusCode: 500, RouterPath: "L1→L2", CreatedAt: now.Add(1 * time.Second)},
		{TokenID: 2, ChannelID: 2, Model: "minimax-text", PromptTokens: 50, CompletionTokens: 25, RealCostUSD: 0.005, BilledCostUSD: 0.005, DurationMs: 50, StatusCode: 200, RouterPath: "L1→L2", CreatedAt: now.Add(2 * time.Second)},
	}
	for _, l := range rows {
		if err := s.CreateLog(l); err != nil {
			t.Fatalf("CreateLog: %v", err)
		}
	}

	n, err := s.CountLogs()
	if err != nil || n != 3 {
		t.Fatalf("CountLogs: n=%d err=%v", n, err)
	}

	st, err := s.LogStats()
	if err != nil {
		t.Fatalf("LogStats: %v", err)
	}
	if st.Total != 3 || st.Errors != 1 {
		t.Fatalf("stats totals: total=%d errors=%d", st.Total, st.Errors)
	}
	if st.PromptTokens != 350 || st.CompletionTokens != 175 {
		t.Fatalf("tokens: %+v", st)
	}
	if st.RealCostUSD < 0.0349 || st.RealCostUSD > 0.0351 {
		t.Fatalf("cost: %.6f", st.RealCostUSD)
	}

	logs, err := s.GetLogs(10, 0)
	if err != nil || len(logs) != 3 {
		t.Fatalf("GetLogs: %d %v", len(logs), err)
	}
	// ORDER BY id DESC: newest first
	if logs[0].CreatedAt.Before(logs[2].CreatedAt) {
		t.Fatal("GetLogs not ordered DESC")
	}
}