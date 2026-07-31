package store

import (
	"context"
	"errors"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

// TestPostgres_NotImplementedForAllMethods documents that the
// Postgres skeleton returns errNotImplemented for every method.
// A real PG backend is out of scope for this codebase — this
// file exists primarily to keep the Store interface satisfied
// for users who want to bring their own backend. The test
// exercises a representative slice of methods so the coverage
// tool sees the skeleton as exercised.
func TestPostgres_NotImplementedForAllMethods(t *testing.T) {
	p := &Postgres{}
	// We avoid methods that touch p.db (Ping/Close) since the
	// skeleton has db=nil and would panic. All other methods
	// just return errNotImplemented without dereferencing.

	checks := []struct {
		name string
		fn   func() error
	}{
		{"GetChannels", func() error { _, err := p.GetChannels(); return err }},
		{"GetChannel", func() error { _, err := p.GetChannel(1); return err }},
		{"CreateChannel", func() error { return p.CreateChannel(&model.Channel{}) }},
		{"UpdateChannel", func() error { return p.UpdateChannel(&model.Channel{}) }},
		{"DeleteChannel", func() error { return p.DeleteChannel(1) }},
		{"GetDrainedChannels", func() error { _, err := p.GetDrainedChannels(); return err }},
		{"GetKeys", func() error { _, err := p.GetKeys(1); return err }},
		{"CreateKey", func() error { return p.CreateKey(&model.Key{}) }},
		{"DeleteKey", func() error { return p.DeleteKey(1) }},
		{"WipeKeys", func() error { _, err := p.WipeKeys(); return err }},
		{"GetToken", func() error { _, err := p.GetToken(""); return err }},
		{"GetTokenByID", func() error { _, err := p.GetTokenByID(1); return err }},
		{"GetTokens", func() error { _, err := p.GetTokens(); return err }},
		{"CreateToken", func() error { return p.CreateToken(&model.Token{}) }},
		{"UpdateToken", func() error { return p.UpdateToken(&model.Token{}) }},
		{"DeleteToken", func() error { return p.DeleteToken(1) }},
		{"IncrementTokenSpend", func() error { return p.IncrementTokenSpend(1, 1.0) }},
		{"IncrementPlanSpend", func() error { return p.IncrementPlanSpend(1, 1.0) }},
		{"MarkTokenExpired", func() error { return p.MarkTokenExpired(1) }},
		{"RecordRequestSpend", func() error { return p.RecordRequestSpend(1, 1, 1.0) }},
		{"GetPlans", func() error { _, err := p.GetPlans(); return err }},
		{"GetPlan", func() error { _, err := p.GetPlan(1); return err }},
		{"CreatePlan", func() error { return p.CreatePlan(&model.Plan{}) }},
		{"UpdatePlan", func() error { return p.UpdatePlan(&model.Plan{}) }},
		{"DeletePlan", func() error { return p.DeletePlan(1) }},
		{"GetUsers", func() error { _, err := p.GetUsers(); return err }},
		{"GetUser", func() error { _, err := p.GetUser(1); return err }},
		{"GetUserByUsername", func() error { _, err := p.GetUserByUsername("x"); return err }},
		{"GetUserBySession", func() error { _, err := p.GetUserBySession("x"); return err }},
		{"CreateUser", func() error { return p.CreateUser(&model.User{}) }},
		{"UpdateUser", func() error { return p.UpdateUser(&model.User{}) }},
		{"CleanupExpiredSessions", func() error { _, err := p.CleanupExpiredSessions(); return err }},
		{"GetAlerts", func() error { _, err := p.GetAlerts(); return err }},
		{"GetAlert", func() error { _, err := p.GetAlert(1); return err }},
		{"CreateAlert", func() error { return p.CreateAlert(&model.Alert{}) }},
		{"UpdateAlert", func() error { return p.UpdateAlert(&model.Alert{}) }},
		{"DeleteAlert", func() error { return p.DeleteAlert(1) }},
		{"RecordAlertFired", func() error { return p.RecordAlertFired(1, 1) }},
		{"DisableAlert", func() error { return p.DisableAlert(1, "x") }},
		{"GetAlertEvents", func() error { _, err := p.GetAlertEvents(10); return err }},
		{"CreateAlertEvent", func() error { return p.CreateAlertEvent(&model.AlertEvent{}) }},
		{"AckAlertEvent", func() error { return p.AckAlertEvent(1) }},
		{"GetRuntimeSettings", func() error { _, err := p.GetRuntimeSettings(); return err }},
		{"SetRuntimeSettings", func() error { return p.SetRuntimeSettings(nil) }},
		{"GetProviderDefs", func() error { _, err := p.GetProviderDefs(); return err }},
		{"CreateProviderDef", func() error { return p.CreateProviderDef(&model.ProviderDef{}) }},
		{"DeleteProviderDef", func() error { return p.DeleteProviderDef(1) }},
		{"GetComboModels", func() error { _, err := p.GetComboModels(1); return err }},
		{"GetComboModel", func() error { _, err := p.GetComboModel(1); return err }},
		{"GetAllComboModels", func() error { _, err := p.GetAllComboModels(); return err }},
		{"ListAllComboModels", func() error { _, err := p.ListAllComboModels(); return err }},
		{"CreateComboModel", func() error { return p.CreateComboModel(&model.TokenComboModel{}) }},
		{"UpdateComboModel", func() error { return p.UpdateComboModel(&model.TokenComboModel{}) }},
		{"DeleteComboModel", func() error { return p.DeleteComboModel(1) }},
		{"SetDefaultModelSet", func() error { return p.SetDefaultModelSet(1, 1) }},
		{"GetEnabledGuardrailRules", func() error { _, err := p.GetEnabledGuardrailRules(); return err }},
		{"GetGuardrailRules", func() error { _, err := p.GetGuardrailRules(); return err }},
		{"GetGuardrailRule", func() error { _, err := p.GetGuardrailRule(1); return err }},
		{"CreateGuardrailRule", func() error { return p.CreateGuardrailRule(&model.GuardrailRule{}) }},
		{"UpdateGuardrailRule", func() error { return p.UpdateGuardrailRule(&model.GuardrailRule{}) }},
		{"DeleteGuardrailRule", func() error { return p.DeleteGuardrailRule(1) }},
		{"CreateGuardrailEvent", func() error { return p.CreateGuardrailEvent(&model.GuardrailEvent{}) }},
		{"GetGuardrailEvents", func() error { _, err := p.GetGuardrailEvents(1, 10); return err }},
		{"ListBYOKChannels", func() error { _, err := p.ListBYOKChannels(context.Background()); return err }},
		{"GetBYOKChannel", func() error { _, err := p.GetBYOKChannel(context.Background(), 1); return err }},
		{"GetBYOKChannelByIP", func() error { _, err := p.GetBYOKChannelByIP(context.Background(), "x"); return err }},
		{"CreateBYOKChannel", func() error { _, err := p.CreateBYOKChannel(context.Background(), &model.BYOKChannel{}); return err }},
		{"TouchBYOKChannel", func() error { return p.TouchBYOKChannel(context.Background(), 1) }},
		{"DeleteBYOKChannel", func() error { return p.DeleteBYOKChannel(context.Background(), 1) }},
		{"ReencryptAllKeys", func() error { _, err := p.ReencryptAllKeys(nil, nil); return err }},
		{"RotateMasterKey", func() error { _, err := p.RotateMasterKey("x"); return err }},
	}
	for _, c := range checks {
		if err := c.fn(); !errors.Is(err, errNotImplemented) {
			t.Errorf("%s: got %v, want errNotImplemented", c.name, err)
		}
	}
}

// TestPostgres_OpenEmptyDSN verifies OpenPostgres rejects an
// empty DSN without trying to connect.
func TestPostgres_OpenEmptyDSN(t *testing.T) {
	if _, err := OpenPostgres(""); err == nil {
		t.Error("expected error for empty dsn")
	}
}