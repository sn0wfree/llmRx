// Package storetest provides the shared semantic test suite for
// store.Store implementations. Any backend (SQLite, Postgres,
// future SQL-family stores) must pass RunSuite before it can be
// considered an adapter — this is the acceptance lever for
// docs/P12-STORE-ABSTRACTION.md.
package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
	"github.com/sn0wfree/llmRx/internal/store"
)

// RunSuite runs the full 88-method semantic suite. Each group gets
// a fresh store instance so failures are isolated. Implementations
// call it like:
//
//	func TestSQLiteSuite(t *testing.T) {
//	    storetest.RunSuite(t, func(t *testing.T) store.Store {
//	        st, err := store.OpenSQLite(t.TempDir() + "/t.db")
//	        if err != nil { t.Fatal(err) }
//	        return st
//	    })
//	}
//
// Backends that share a physical database between groups (Postgres)
// pass a reset function that empties all tables before each group.
func RunSuite(t *testing.T, newStore func(t *testing.T) store.Store, reset ...func(t *testing.T, st store.Store)) {
	var doReset func(t *testing.T, st store.Store)
	if len(reset) > 0 {
		doReset = reset[0]
	}
	run := func(name string, fn func(t *testing.T, st store.Store)) {
		t.Run(name, func(t *testing.T) {
			st := newStore(t)
			if doReset != nil {
				doReset(t, st)
			}
			fn(t, st)
			_ = st.Close()
		})
	}
	run("Ping", func(t *testing.T, st store.Store) { testPing(t, st) })
	run("Channels", func(t *testing.T, st store.Store) { testChannels(t, st) })
	run("Keys", func(t *testing.T, st store.Store) { testKeys(t, st) })
	run("Tokens", func(t *testing.T, st store.Store) { testTokens(t, st) })
	run("Plans", func(t *testing.T, st store.Store) { testPlans(t, st) })
	run("Users", func(t *testing.T, st store.Store) { testUsers(t, st) })
	run("Alerts", func(t *testing.T, st store.Store) { testAlerts(t, st) })
	run("Guardrails", func(t *testing.T, st store.Store) { testGuardrails(t, st) })
	run("BYOK", func(t *testing.T, st store.Store) { testBYOK(t, st) })
	run("Providers", func(t *testing.T, st store.Store) { testProviders(t, st) })
	run("Combos", func(t *testing.T, st store.Store) { testCombos(t, st) })
	run("Runtime", func(t *testing.T, st store.Store) { testRuntime(t, st) })
	run("Security", func(t *testing.T, st store.Store) { testSecurity(t, st) })
	run("MCP", func(t *testing.T, st store.Store) { testMCP(t, st) })
}

func testPing(t *testing.T, st store.Store) {
	if err := st.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// ---------- Channels (6 methods) ----------

func testChannels(t *testing.T, st store.Store) {
	now := time.Now().UTC()
	ch := &model.Channel{
		Name:                "ch1",
		Provider:            "openai",
		Protocol:            "openai",
		BaseURL:             "https://api.openai.com/v1",
		Models:              []string{"gpt-4o"},
		Intents:             []string{"chat"},
		Priority:            5,
		InputPrice:          2.5,
		OutputPrice:         10,
		CachedInputDiscount: 0.1,
		Status:              model.ChannelEnabled,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if ch.ID == 0 {
		t.Fatal("CreateChannel: ID not assigned")
	}
	got, err := st.GetChannel(ch.ID)
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if got.Name != ch.Name || got.Provider != ch.Provider ||
		len(got.Models) != 1 || got.Models[0] != "gpt-4o" ||
		got.Intents[0] != "chat" || got.Status != model.ChannelEnabled {
		t.Fatalf("GetChannel roundtrip mismatch: %+v", got)
	}

	list, err := st.GetChannels()
	if err != nil {
		t.Fatalf("GetChannels: %v", err)
	}
	if len(list) != 1 || list[0].ID != ch.ID {
		t.Fatalf("GetChannels: got %d rows, want 1", len(list))
	}

	// Update keeps the same id and bumps updated_at semantics.
	ch.Priority = 9
	ch.Provider = "anthropic"
	if err := st.UpdateChannel(ch); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	got, err = st.GetChannel(ch.ID)
	if err != nil {
		t.Fatalf("GetChannel after update: %v", err)
	}
	if got.Priority != 9 || got.Provider != "anthropic" {
		t.Fatalf("UpdateChannel not reflected: %+v", got)
	}

	// GetChannel on missing id -> ErrNotFound.
	if _, err := st.GetChannel(999999); err != store.ErrNotFound {
		t.Fatalf("GetChannel(999999) err = %v, want ErrNotFound", err)
	}

	// CreateChannel with duplicate name -> error (UNIQUE violation;
	// the error type is backend-specific).
	dup := &model.Channel{Name: "ch1", Provider: "openai", BaseURL: "x", CreatedAt: now, UpdatedAt: now}
	if err := st.CreateChannel(dup); err == nil {
		t.Fatalf("duplicate CreateChannel should fail")
	}

	// Drained: a channel with an active key is NOT drained.
	kID := int64(0)
	{
		kc := &model.Key{ChannelID: ch.ID, Key: "k-active", KeyMasked: "k-…", Status: model.KeyActive, CreatedAt: now}
		if err := st.CreateKey(kc); err != nil {
			t.Fatalf("CreateKey: %v", err)
		}
		kID = kc.ID
	}
	drained, err := st.GetDrainedChannels()
	if err != nil {
		t.Fatalf("GetDrainedChannels: %v", err)
	}
	for _, d := range drained {
		if d.ID == ch.ID {
			t.Fatalf("channel with active keys should not be drained")
		}
	}
	// Removing the key makes the channel drained.
	if err := st.DeleteKey(kID); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	drained, err = st.GetDrainedChannels()
	if err != nil {
		t.Fatalf("GetDrainedChannels: %v", err)
	}
	found := false
	for _, d := range drained {
		if d.ID == ch.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("channel with no active keys should be drained")
	}

	if err := st.DeleteChannel(ch.ID); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}
	if _, err := st.GetChannel(ch.ID); err != store.ErrNotFound {
		t.Fatalf("GetChannel after delete err = %v, want ErrNotFound", err)
	}
}

// ---------- Keys (4 methods) ----------

func testKeys(t *testing.T, st store.Store) {
	now := time.Now().UTC()
	ch := &model.Channel{Name: "kch", Provider: "openai", BaseURL: "x", CreatedAt: now, UpdatedAt: now}
	if err := st.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}

	k := &model.Key{ChannelID: ch.ID, Key: "sk-abc123", KeyMasked: "sk-…123", Status: model.KeyActive, CreatedAt: now}
	if err := st.CreateKey(k); err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if k.ID == 0 {
		t.Fatal("CreateKey: ID not assigned")
	}

	keys, err := st.GetKeys(ch.ID)
	if err != nil {
		t.Fatalf("GetKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("GetKeys: got %d, want 1", len(keys))
	}
	if keys[0].Key != "sk-abc123" {
		t.Fatalf("GetKeys: key not roundtripped: %q", keys[0].Key)
	}

	if err := st.DeleteKey(k.ID); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	if keys, _ := st.GetKeys(ch.ID); len(keys) != 0 {
		t.Fatalf("keys after delete: %d", len(keys))
	}

	// WipeKeys clears material but keeps shells.
	if err := st.CreateKey(&model.Key{ChannelID: ch.ID, Key: "sk-xyz", KeyMasked: "sk-…", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if n, err := st.WipeKeys(); err != nil || n != 1 {
		t.Fatalf("WipeKeys: n=%d err=%v, want 1", n, err)
	}
	keys, _ = st.GetKeys(ch.ID)
	if len(keys) != 1 || keys[0].Key != "" {
		t.Fatalf("WipeKeys left material: %+v", keys)
	}
}

// ---------- Tokens (8 methods) ----------

func testTokens(t *testing.T, st store.Store) {
	now := time.Now().UTC()
	tk := &model.Token{
		Key:             "tok-secret-1",
		Name:            "t1",
		Status:          model.TokenActive,
		RPM:             60,
		TPM:             100000,
		ModelsWhitelist: []string{"gpt-4o"},
		IPWhitelist:     []string{"127.0.0.1"},
		CreatedAt:       now,
	}
	if err := st.CreateToken(tk); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if tk.ID == 0 {
		t.Fatal("CreateToken: ID not assigned")
	}

	if got, err := st.GetToken(tk.Key); err != nil || got.ID != tk.ID {
		t.Fatalf("GetToken: err=%v got=%+v", err, got)
	}
	if got, err := st.GetTokenByID(tk.ID); err != nil || got.Name != "t1" {
		t.Fatalf("GetTokenByID: err=%v", err)
	}
	toks, err := st.GetTokens()
	if err != nil || len(toks) != 1 {
		t.Fatalf("GetTokens: err=%v n=%d", err, len(toks))
	}

	// Update.
	tk.RPM = 120
	tk.Name = "t1-renamed"
	if err := st.UpdateToken(tk); err != nil {
		t.Fatalf("UpdateToken: %v", err)
	}
	if got, _ := st.GetTokenByID(tk.ID); got.RPM != 120 || got.Name != "t1-renamed" {
		t.Fatalf("UpdateToken not reflected: %+v", got)
	}

	// IncrementTokenSpend.
	if err := st.IncrementTokenSpend(tk.ID, 0.5); err != nil {
		t.Fatalf("IncrementTokenSpend: %v", err)
	}
	if got, _ := st.GetTokenByID(tk.ID); got.UsedUSD != 0.5 {
		t.Fatalf("IncrementTokenSpend: used_usd = %v, want 0.5", got.UsedUSD)
	}
	if err := st.IncrementTokenSpend(999999, 1); err != store.ErrNotFound {
		t.Fatalf("IncrementTokenSpend missing: err=%v, want ErrNotFound", err)
	}

	// MarkTokenExpired.
	if err := st.MarkTokenExpired(tk.ID); err != nil {
		t.Fatalf("MarkTokenExpired: %v", err)
	}
	if got, _ := st.GetTokenByID(tk.ID); got.Status != model.TokenExpired {
		t.Fatalf("MarkTokenExpired: status=%v, want expired", got.Status)
	}

	if err := st.DeleteToken(tk.ID); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}
	if _, err := st.GetTokenByID(tk.ID); err != store.ErrNotFound {
		t.Fatalf("GetTokenByID after delete err=%v", err)
	}
}

// ---------- Plans (7 methods) ----------

func testPlans(t *testing.T, st store.Store) {
	now := time.Now().UTC()
	p := &model.Plan{Name: "p1", BudgetUSD: 100, UsedUSD: 0, MarkupRatio: 1.2, Status: 1, CreatedAt: now, UpdatedAt: now}
	if err := st.CreatePlan(p); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if p.ID == 0 {
		t.Fatal("CreatePlan: ID not assigned")
	}

	plans, err := st.GetPlans()
	if err != nil || len(plans) != 1 {
		t.Fatalf("GetPlans: err=%v n=%d", err, len(plans))
	}
	if got, err := st.GetPlan(p.ID); err != nil || got.Name != "p1" {
		t.Fatalf("GetPlan: err=%v", err)
	}

	p.MarkupRatio = 2.0
	if err := st.UpdatePlan(p); err != nil {
		t.Fatalf("UpdatePlan: %v", err)
	}
	if got, _ := st.GetPlan(p.ID); got.MarkupRatio != 2.0 {
		t.Fatalf("UpdatePlan not reflected")
	}

	// IncrementPlanSpend within budget.
	if err := st.IncrementPlanSpend(p.ID, 10); err != nil {
		t.Fatalf("IncrementPlanSpend: %v", err)
	}
	// Over budget -> ErrBudgetExceeded.
	if err := st.IncrementPlanSpend(p.ID, 200); err != store.ErrBudgetExceeded {
		t.Fatalf("IncrementPlanSpend over budget: err=%v, want ErrBudgetExceeded", err)
	}

	// RecordRequestSpend touches token + plan ledgers.
	tk := &model.Token{Key: "tok-spend", Status: model.TokenActive, PlanID: p.ID, CreatedAt: now}
	if err := st.CreateToken(tk); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordRequestSpend(tk.ID, p.ID, 5); err != nil {
		t.Fatalf("RecordRequestSpend: %v", err)
	}
	if got, _ := st.GetTokenByID(tk.ID); got.UsedUSD != 5 {
		t.Fatalf("RecordRequestSpend token ledger: %v", got.UsedUSD)
	}
	if got, _ := st.GetPlan(p.ID); got.UsedUSD != 15 {
		t.Fatalf("RecordRequestSpend plan ledger: %v", got.UsedUSD)
	}

	if err := st.DeletePlan(p.ID); err != nil {
		t.Fatalf("DeletePlan: %v", err)
	}
	if _, err := st.GetPlan(p.ID); err != store.ErrNotFound {
		t.Fatalf("GetPlan after delete err=%v", err)
	}
}

// ---------- Users (7 methods) ----------

func testUsers(t *testing.T, st store.Store) {
	exp := time.Now().UTC().Add(time.Hour)
	u := &model.User{Username: "alice", PasswordHash: "h1", Role: model.RoleAdmin, Status: 1, SessionToken: "sess-1", SessionExp: &exp, CreatedAt: time.Now().UTC()}
	if err := st.CreateUser(u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID == 0 {
		t.Fatal("CreateUser: ID not assigned")
	}

	users, err := st.GetUsers()
	if err != nil || len(users) != 1 {
		t.Fatalf("GetUsers: err=%v n=%d", err, len(users))
	}
	if got, err := st.GetUser(u.ID); err != nil || got.Username != "alice" {
		t.Fatalf("GetUser: err=%v", err)
	}
	if got, err := st.GetUserByUsername("alice"); err != nil || got.ID != u.ID {
		t.Fatalf("GetUserByUsername: err=%v", err)
	}
	if got, err := st.GetUserBySession("sess-1"); err != nil || got.ID != u.ID {
		t.Fatalf("GetUserBySession: err=%v", err)
	}

	u.SessionToken = "sess-2"
	u.Status = 0
	if err := st.UpdateUser(u); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if got, _ := st.GetUser(u.ID); got.Status != 0 || got.SessionToken != "sess-2" {
		t.Fatalf("UpdateUser not reflected")
	}

	// Expired session cleanup: create a user with an already-expired
	// session and expect CleanupExpiredSessions to remove it.
	past := time.Now().UTC().Add(-time.Hour)
	u2 := &model.User{Username: "bob", PasswordHash: "h2", Status: 1, SessionToken: "sess-old", SessionExp: &past, CreatedAt: time.Now().UTC()}
	if err := st.CreateUser(u2); err != nil {
		t.Fatal(err)
	}
	if n, err := st.CleanupExpiredSessions(); err != nil || n != 1 {
		t.Fatalf("CleanupExpiredSessions: n=%d err=%v, want 1", n, err)
	}
}

// ---------- Alerts (10 methods) ----------

func testAlerts(t *testing.T, st store.Store) {
	now := time.Now().UTC()
	a := &model.Alert{
		Name: "high-latency", Type: model.AlertP95Latency, Threshold: 2000,
		WindowSec: 60, CooldownSec: 300, WebhookURL: "https://hook", Enabled: true, CreatedAt: now,
	}
	if err := st.CreateAlert(a); err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}
	if a.ID == 0 {
		t.Fatal("CreateAlert: ID not assigned")
	}

	alerts, err := st.GetAlerts()
	if err != nil || len(alerts) != 1 {
		t.Fatalf("GetAlerts: err=%v n=%d", err, len(alerts))
	}
	if got, err := st.GetAlert(a.ID); err != nil || !got.Enabled || got.Threshold != 2000 {
		t.Fatalf("GetAlert: err=%v got=%+v", err, got)
	}

	a.Threshold = 4000
	if err := st.UpdateAlert(a); err != nil {
		t.Fatalf("UpdateAlert: %v", err)
	}
	if got, _ := st.GetAlert(a.ID); got.Threshold != 4000 {
		t.Fatalf("UpdateAlert not reflected")
	}

	// RecordAlertFired advances last_fired_at and wins the claim.
	changed, err := st.RecordAlertFired(a.ID, now.Add(time.Minute).Unix())
	if err != nil {
		t.Fatalf("RecordAlertFired: %v", err)
	}
	if !changed {
		t.Fatal("first claim must win")
	}
	// A second claim with an older timestamp loses (no double fire).
	changed, err = st.RecordAlertFired(a.ID, now.Add(time.Second).Unix())
	if err != nil {
		t.Fatalf("RecordAlertFired(old): %v", err)
	}
	if changed {
		t.Fatal("older claim must not win")
	}

	// DisableAlert flips enabled and records reason.
	if err := st.DisableAlert(a.ID, "cooldown"); err != nil {
		t.Fatalf("DisableAlert: %v", err)
	}
	if got, _ := st.GetAlert(a.ID); got.Enabled || got.DisabledReason != "cooldown" {
		t.Fatalf("DisableAlert not reflected: %+v", got)
	}

	// Alert events.
	e := &model.AlertEvent{AlertID: a.ID, AlertName: a.Name, AlertType: a.Type, FiredAt: now, Payload: "{}", DeliveredWebhook: true, Acknowledged: false}
	if err := st.CreateAlertEvent(e); err != nil {
		t.Fatalf("CreateAlertEvent: %v", err)
	}
	events, err := st.GetAlertEvents(10)
	if err != nil || len(events) != 1 {
		t.Fatalf("GetAlertEvents: err=%v n=%d", err, len(events))
	}
	if !events[0].DeliveredWebhook || events[0].Acknowledged {
		t.Fatalf("alert event roundtrip mismatch: %+v", events[0])
	}
	if err := st.AckAlertEvent(e.ID); err != nil {
		t.Fatalf("AckAlertEvent: %v", err)
	}
	if ev, _ := st.GetAlertEvents(10); !ev[0].Acknowledged {
		t.Fatalf("AckAlertEvent not reflected")
	}

	if err := st.DeleteAlert(a.ID); err != nil {
		t.Fatalf("DeleteAlert: %v", err)
	}
	if _, err := st.GetAlert(a.ID); err != store.ErrNotFound {
		t.Fatalf("GetAlert after delete err=%v", err)
	}
}

// ---------- Guardrails (8 methods) ----------

func testGuardrails(t *testing.T, st store.Store) {
	now := time.Now().UTC()
	r := &model.GuardrailRule{
		Name: "block-credit", Description: "no credit card numbers",
		Type: model.GuardrailRegexBlock, Hook: model.GuardrailHookInput,
		OnFailure: model.GuardrailActionDeny, Config: `{"pattern": "\\d{16}"}`,
		Priority: 100, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateGuardrailRule(r); err != nil {
		t.Fatalf("CreateGuardrailRule: %v", err)
	}
	if r.ID == 0 {
		t.Fatal("CreateGuardrailRule: ID not assigned")
	}

	rules, err := st.GetGuardrailRules()
	if err != nil || len(rules) != 1 {
		t.Fatalf("GetGuardrailRules: err=%v n=%d", err, len(rules))
	}
	enabled, err := st.GetEnabledGuardrailRules()
	if err != nil || len(enabled) != 1 {
		t.Fatalf("GetEnabledGuardrailRules: err=%v n=%d", err, len(enabled))
	}
	if got, err := st.GetGuardrailRule(r.ID); err != nil || got.Type != model.GuardrailRegexBlock || !got.Enabled {
		t.Fatalf("GetGuardrailRule: err=%v got=%+v", err, got)
	}

	r.Priority = 50
	r.Enabled = false
	if err := st.UpdateGuardrailRule(r); err != nil {
		t.Fatalf("UpdateGuardrailRule: %v", err)
	}
	if got, _ := st.GetGuardrailRule(r.ID); got.Priority != 50 || got.Enabled {
		t.Fatalf("UpdateGuardrailRule not reflected")
	}
	if enabled, _ := st.GetEnabledGuardrailRules(); len(enabled) != 0 {
		t.Fatalf("disabled rule still listed as enabled")
	}

	// Events.
	e := &model.GuardrailEvent{TokenID: 1, RuleID: r.ID, RuleName: r.Name, RuleType: string(r.Type), Hook: string(r.Hook), Verdict: true, Action: "deny", Detail: "match", RequestIP: "127.0.0.1", CreatedAt: now}
	if err := st.CreateGuardrailEvent(e); err != nil {
		t.Fatalf("CreateGuardrailEvent: %v", err)
	}
	events, err := st.GetGuardrailEvents(1, 10)
	if err != nil || len(events) != 1 || !events[0].Verdict {
		t.Fatalf("GetGuardrailEvents: err=%v events=%+v", err, events)
	}

	if err := st.DeleteGuardrailRule(r.ID); err != nil {
		t.Fatalf("DeleteGuardrailRule: %v", err)
	}
	if _, err := st.GetGuardrailRule(r.ID); err != store.ErrNotFound {
		t.Fatalf("GetGuardrailRule after delete err=%v", err)
	}
}

// ---------- BYOK (6 methods) ----------

func testBYOK(t *testing.T, st store.Store) {
	now := time.Now().UTC()
	ch := &model.BYOKChannel{Provider: "openai", KeyCiphertext: "enc", KeyMasked: "sk-…", OwnerIP: "1.2.3.4", OwnerEmail: "a@b.c", Status: 1, LastUsedAt: now, ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now}
	id, err := st.CreateBYOKChannel(context.Background(), ch)
	if err != nil {
		t.Fatalf("CreateBYOKChannel: %v", err)
	}
	if id == 0 {
		t.Fatal("CreateBYOKChannel: ID not assigned")
	}

	if got, err := st.GetBYOKChannel(context.Background(), id); err != nil || got.KeyMasked != "sk-…" {
		t.Fatalf("GetBYOKChannel: err=%v", err)
	}
	if got, err := st.GetBYOKChannelByIP(context.Background(), "1.2.3.4"); err != nil || got.ID != id {
		t.Fatalf("GetBYOKChannelByIP: err=%v", err)
	}
	if _, err := st.GetBYOKChannelByIP(context.Background(), "9.9.9.9"); err != store.ErrNotFound {
		t.Fatalf("GetBYOKChannelByIP missing err=%v", err)
	}
	all, err := st.ListBYOKChannels(context.Background())
	if err != nil || len(all) != 1 {
		t.Fatalf("ListBYOKChannels: err=%v n=%d", err, len(all))
	}

	if err := st.TouchBYOKChannel(context.Background(), id); err != nil {
		t.Fatalf("TouchBYOKChannel: %v", err)
	}
	if got, _ := st.GetBYOKChannel(context.Background(), id); got.UseCount != 1 {
		t.Fatalf("TouchBYOKChannel: use_count=%d, want 1", got.UseCount)
	}

	if err := st.DeleteBYOKChannel(context.Background(), id); err != nil {
		t.Fatalf("DeleteBYOKChannel: %v", err)
	}
	if _, err := st.GetBYOKChannel(context.Background(), id); err != store.ErrNotFound {
		t.Fatalf("GetBYOKChannel after delete err=%v", err)
	}
}

// ---------- Providers (3 methods) ----------

func testProviders(t *testing.T, st store.Store) {
	p := &model.ProviderDef{Name: "my-proxy", DisplayName: "My Proxy", Protocol: "openai", BaseURL: "https://proxy.example.com"}
	if err := st.CreateProviderDef(p); err != nil {
		t.Fatalf("CreateProviderDef: %v", err)
	}
	if p.ID == 0 {
		t.Fatal("CreateProviderDef: ID not assigned")
	}

	defs, err := st.GetProviderDefs()
	if err != nil || len(defs) != 1 || defs[0].Name != "my-proxy" {
		t.Fatalf("GetProviderDefs: err=%v defs=%+v", err, defs)
	}

	if err := st.DeleteProviderDef(p.ID); err != nil {
		t.Fatalf("DeleteProviderDef: %v", err)
	}
	if defs, _ := st.GetProviderDefs(); len(defs) != 0 {
		t.Fatalf("defs after delete: %d", len(defs))
	}
}

// ---------- Combos (8 methods) ----------

func testCombos(t *testing.T, st store.Store) {
	now := time.Now().UTC()
	tk := &model.Token{Key: "tok-combo", Status: model.TokenActive, CreatedAt: now}
	if err := st.CreateToken(tk); err != nil {
		t.Fatal(err)
	}

	c := &model.TokenComboModel{TokenID: tk.ID, Name: "combo-a", Models: []string{"gpt-4o", "claude-3.5"}, Mode: model.ComboModeLoadBalance, Strategy: model.StrategyBalanced, Enabled: true, IsDefault: false, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateComboModel(c); err != nil {
		t.Fatalf("CreateComboModel: %v", err)
	}
	if c.ID == 0 {
		t.Fatal("CreateComboModel: ID not assigned")
	}

	// mode:auto combo with a tier table must round-trip (SQLite and
	// Postgres both persist the JSON tiers column).
	auto := &model.TokenComboModel{TokenID: tk.ID, Name: "combo-auto", Mode: model.ComboModeAuto, Enabled: true, CreatedAt: now, UpdatedAt: now,
		Tiers: map[string]model.TierConfig{
			"simple":   {Models: []string{"deepseek-chat", "gpt-4o-mini"}},
			"standard": {Models: []string{"deepseek-chat", "gpt-4o"}},
			"complex":  {Models: []string{"gpt-4o", "claude-3.5"}},
			"agentic":  {Models: []string{"claude-3.5"}},
		},
		Fallback: []string{"deepseek-chat"},
	}
	if err := st.CreateComboModel(auto); err != nil {
		t.Fatalf("CreateComboModel(auto): %v", err)
	}
	autoGot, err := st.GetComboModel(auto.ID)
	if err != nil {
		t.Fatalf("GetComboModel(auto): %v", err)
	}
	if autoGot.Mode != model.ComboModeAuto || len(autoGot.Tiers) != 4 {
		t.Fatalf("auto combo not round-tripped: %+v", autoGot)
	}
	if autoGot.Tiers["simple"].Models[0] != "deepseek-chat" || autoGot.Tiers["agentic"].Models[0] != "claude-3.5" {
		t.Fatalf("auto tiers content wrong: %+v", autoGot.Tiers)
	}
	if len(autoGot.Fallback) != 1 || autoGot.Fallback[0] != "deepseek-chat" {
		t.Fatalf("auto fallback wrong: %+v", autoGot.Fallback)
	}

	combos, err := st.GetComboModels(tk.ID)
	if err != nil || len(combos) != 2 || len(combos[0].Models) != 2 {
		t.Fatalf("GetComboModels: err=%v combos=%+v", err, combos)
	}
	if got, err := st.GetComboModel(c.ID); err != nil || got.Name != "combo-a" {
		t.Fatalf("GetComboModel: err=%v", err)
	}
	if all, err := st.GetAllComboModels(); err != nil || len(all) != 2 {
		t.Fatalf("GetAllComboModels: err=%v n=%d", err, len(all))
	}
	if all, err := st.ListAllComboModels(); err != nil || len(all) != 2 {
		t.Fatalf("ListAllComboModels: err=%v n=%d", err, len(all))
	}

	// SetDefaultModelSet: explicit comboID sets the default flag.
	if err := st.SetDefaultModelSet(tk.ID, c.ID); err != nil {
		t.Fatalf("SetDefaultModelSet: %v", err)
	}
	if combos, _ := st.GetComboModels(tk.ID); !combos[0].IsDefault {
		t.Fatalf("SetDefaultModelSet(id) should mark the combo default")
	}

	c2 := &model.TokenComboModel{TokenID: tk.ID, Name: "combo-b", Models: []string{"gpt-4o-mini"}, Mode: model.ComboModeLoadBalance, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateComboModel(c2); err != nil {
		t.Fatal(err)
	}
	// Setting c2 default clears c1.
	if err := st.SetDefaultModelSet(tk.ID, c2.ID); err != nil {
		t.Fatalf("SetDefaultModelSet(id): %v", err)
	}
	// Clearing via comboID=0 resets all defaults.
	if err := st.SetDefaultModelSet(tk.ID, 0); err != nil {
		t.Fatalf("SetDefaultModelSet(0): %v", err)
	}
	if err := st.SetDefaultModelSet(tk.ID, c2.ID); err != nil {
		t.Fatalf("SetDefaultModelSet(id): %v", err)
	}
	combos, _ = st.GetComboModels(tk.ID)
	for _, cc := range combos {
		if cc.ID == c2.ID && !cc.IsDefault {
			t.Fatalf("c2 should be default")
		}
		if cc.ID == c.ID && cc.IsDefault {
			t.Fatalf("c1 should not be default")
		}
	}

	c2.Enabled = false
	if err := st.UpdateComboModel(c2); err != nil {
		t.Fatalf("UpdateComboModel: %v", err)
	}
	if got, _ := st.GetComboModel(c2.ID); got.Enabled {
		t.Fatalf("UpdateComboModel not reflected")
	}

	if err := st.DeleteComboModel(c.ID); err != nil {
		t.Fatalf("DeleteComboModel: %v", err)
	}
	if _, err := st.GetComboModel(c.ID); err != store.ErrNotFound {
		t.Fatalf("GetComboModel after delete err=%v", err)
	}
}

// ---------- Runtime (2 methods) ----------

func testRuntime(t *testing.T, st store.Store) {
	payload := []byte(`{"routing_strategy": "cost"}`)
	if err := st.SetRuntimeSettings(payload); err != nil {
		t.Fatalf("SetRuntimeSettings: %v", err)
	}
	got, err := st.GetRuntimeSettings()
	if err != nil {
		t.Fatalf("GetRuntimeSettings: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("runtime roundtrip: got %s, want %s", got, payload)
	}
	// Overwrite.
	payload2 := []byte(`{"routing_strategy": "priority"}`)
	if err := st.SetRuntimeSettings(payload2); err != nil {
		t.Fatalf("SetRuntimeSettings(2): %v", err)
	}
	if got, _ := st.GetRuntimeSettings(); string(got) != string(payload2) {
		t.Fatalf("runtime overwrite failed")
	}
}

// ---------- Security (3 methods) ----------

func testSecurity(t *testing.T, st store.Store) {
	oldMgr, err := secrets.FromHexKey("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	if err != nil {
		t.Fatalf("FromHexKey(old): %v", err)
	}
	newMgr, err := secrets.FromHexKey("202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f")
	if err != nil {
		t.Fatalf("FromHexKey(new): %v", err)
	}

	// Store-level: SetSecrets is part of the interface; SQLite
	// implements SecretsProvider. ReencryptAllKeys works on rows
	// created while a manager is attached.
	// Attach a secrets manager so new keys are stored encrypted
	// (ciphertext) and ReencryptAllKeys can rotate them. Backends
	// without SecretsProvider skip the rotation half.
	_, ok := st.(store.SecretsProvider)
	if ok {
		st.SetSecrets(oldMgr)
	}
	now := time.Now().UTC()
	ch := &model.Channel{Name: "secch", Provider: "openai", BaseURL: "x", CreatedAt: now, UpdatedAt: now}
	if err := st.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateKey(&model.Key{ChannelID: ch.ID, Key: "sk-sec", KeyMasked: "sk-…", Status: model.KeyActive, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateToken(&model.Token{Key: "tok-sec", Status: model.TokenActive, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	// ReencryptAllKeys rotates ciphertext rows from oldMgr to newMgr.
	if ok {
		// RotateMasterKey re-encrypts every table's ciphertext from
		// the store's current manager to a brand-new key and swaps
		// the manager internally.
		if _, err := st.RotateMasterKey("303132333435363738393a3b3c3d3e3f404142434445464748494a4b4c4d4e4f"); err != nil {
			t.Fatalf("RotateMasterKey: %v", err)
		}
		if got, _ := st.GetKeys(ch.ID); len(got) != 1 || got[0].Key != "sk-sec" {
			t.Fatalf("key lost after rotate: %+v", got)
		}
		// ReencryptAllKeys rotates the key table's ciphertext only
		// (a manual pass); the caller must swap the manager before
		// reading again.
		cur := st.(store.SecretsProvider).SecretsManager()
		if cur == nil {
			t.Fatal("SecretsProvider returned nil manager")
		}
		if n, err := st.ReencryptAllKeys(cur, newMgr); err != nil {
			t.Fatalf("ReencryptAllKeys: %v", err)
		} else if n != 1 {
			t.Fatalf("ReencryptAllKeys: n=%d, want 1", n)
		}
		st.SetSecrets(newMgr)
		if got, _ := st.GetKeys(ch.ID); len(got) != 1 || got[0].Key != "sk-sec" {
			t.Fatalf("key lost after reencrypt: %+v", got)
		}
	}
}

// ---------- MCP (11 methods) ----------

func testMCP(t *testing.T, st store.Store) {
	ctx := context.Background()
	srv := &store.MCPServer{Name: "mcp-github", URL: "https://mcp.example.com", AuthHdr: "Bearer x", Transport: "http", Enabled: true}
	if err := st.CreateMCPServer(ctx, srv); err != nil {
		t.Fatalf("CreateMCPServer: %v", err)
	}
	if srv.ID == 0 {
		t.Fatal("CreateMCPServer: ID not assigned")
	}

	servers, err := st.GetMCPServers(ctx)
	if err != nil || len(servers) != 1 {
		t.Fatalf("GetMCPServers: err=%v n=%d", err, len(servers))
	}
	if got, err := st.GetMCPServer(ctx, srv.ID); err != nil || !got.Enabled {
		t.Fatalf("GetMCPServer: err=%v", err)
	}
	if enabled, err := st.GetEnabledMCPServers(ctx); err != nil || len(enabled) != 1 {
		t.Fatalf("GetEnabledMCPServers: err=%v n=%d", err, len(enabled))
	}

	srv.Enabled = false
	srv.Command = "npx @modelcontextprotocol/server-github"
	if err := st.UpdateMCPServer(ctx, srv); err != nil {
		t.Fatalf("UpdateMCPServer: %v", err)
	}
	if got, _ := st.GetMCPServer(ctx, srv.ID); got.Enabled || got.Transport != "http" || got.Command == "" {
		t.Fatalf("UpdateMCPServer not reflected: %+v", got)
	}
	if enabled, _ := st.GetEnabledMCPServers(ctx); len(enabled) != 0 {
		t.Fatalf("disabled server listed as enabled")
	}

	// Tools + pricing.
	tools := []store.MCPTool{
		{ServerID: srv.ID, Name: "list_repos", Description: "list repos", InputSchemaJSON: `{}`},
		{ServerID: srv.ID, Name: "create_issue", Description: "create issue", InputSchemaJSON: `{"type":"object"}`},
	}
	if err := st.SetMCPTools(ctx, srv.ID, tools); err != nil {
		t.Fatalf("SetMCPTools: %v", err)
	}
	gotTools, err := st.GetMCPTools(ctx, srv.ID)
	if err != nil || len(gotTools) != 2 {
		t.Fatalf("GetMCPTools: err=%v n=%d", err, len(gotTools))
	}
	// GetAllMCPTools joins enabled servers only — the server was
	// disabled above, so its tools are excluded.
	if all, err := st.GetAllMCPTools(ctx); err != nil || len(all) != 0 {
		t.Fatalf("GetAllMCPTools(disabled server): err=%v n=%d, want 0", err, len(all))
	}

	price := &store.MCPToolPricing{MCPToolID: gotTools[0].ID, PricePerCallUSD: 0.01}
	if err := st.SetMCPToolPricing(ctx, price); err != nil {
		t.Fatalf("SetMCPToolPricing: %v", err)
	}
	if got, err := st.GetMCPToolPricing(ctx, gotTools[0].ID); err != nil || got.PricePerCallUSD != 0.01 {
		t.Fatalf("GetMCPToolPricing: err=%v got=%+v", err, got)
	}
	// Upsert: overwrite.
	price.PricePerCallUSD = 0.02
	if err := st.SetMCPToolPricing(ctx, price); err != nil {
		t.Fatalf("SetMCPToolPricing(2): %v", err)
	}
	if got, _ := st.GetMCPToolPricing(ctx, gotTools[0].ID); got.PricePerCallUSD != 0.02 {
		t.Fatalf("pricing upsert failed: %+v", got)
	}

	if err := st.DeleteMCPServer(ctx, srv.ID); err != nil {
		t.Fatalf("DeleteMCPServer: %v", err)
	}
	if got, err := st.GetMCPServer(ctx, srv.ID); err != nil || got != nil {
		t.Fatalf("GetMCPServer after delete: err=%v got=%+v (want nil)", err, got)
	}
	// Cascade: tools gone with the server.
	if tools, _ := st.GetMCPTools(ctx, srv.ID); len(tools) != 0 {
		t.Fatalf("tools not cascaded on server delete: %d", len(tools))
	}
}
