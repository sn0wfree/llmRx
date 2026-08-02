package store

import (
	"errors"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestComboModels_AutoTiers_RoundTrip(t *testing.T) {
	s := openTemp(t)

	c := &model.TokenComboModel{
		TokenID: 1,
		Name:    "auto",
		Mode:    model.ComboModeAuto,
		Enabled: true,
		Tiers: map[string]model.TierConfig{
			"simple":   {Models: []string{"deepseek-chat", "gpt-4o-mini"}},
			"standard": {Models: []string{"deepseek-chat", "gpt-4o"}},
			"complex":  {Models: []string{"gpt-4o", "claude-3-5-sonnet"}},
			"agentic":  {Models: []string{"claude-3-5-sonnet"}},
		},
		Fallback: []string{"deepseek-chat"},
	}
	if err := s.CreateComboModel(c); err != nil {
		t.Fatalf("CreateComboModel: %v", err)
	}

	got, err := s.GetComboModel(c.ID)
	if err != nil {
		t.Fatalf("GetComboModel: %v", err)
	}
	if got.Mode != model.ComboModeAuto {
		t.Errorf("mode: got %q, want auto", got.Mode)
	}
	if len(got.Tiers) != 4 {
		t.Fatalf("tiers: got %d entries, want 4: %+v", len(got.Tiers), got.Tiers)
	}
	simple := got.Tiers["simple"]
	if len(simple.Models) != 2 || simple.Models[0] != "deepseek-chat" {
		t.Errorf("simple tier: %+v", simple)
	}
	if got.Tiers["agentic"].Models[0] != "claude-3-5-sonnet" {
		t.Errorf("agentic tier: %+v", got.Tiers["agentic"])
	}
	if len(got.Fallback) != 1 || got.Fallback[0] != "deepseek-chat" {
		t.Errorf("fallback: %+v", got.Fallback)
	}

	// Update: replace the tier table.
	got.Tiers = map[string]model.TierConfig{"simple": {Models: []string{"m1"}}}
	got.Fallback = nil
	if err := s.UpdateComboModel(got); err != nil {
		t.Fatalf("UpdateComboModel: %v", err)
	}
	updated, _ := s.GetComboModel(c.ID)
	if len(updated.Tiers) != 1 || updated.Tiers["simple"].Models[0] != "m1" {
		t.Errorf("updated tiers: %+v", updated.Tiers)
	}
	if len(updated.Fallback) != 0 {
		t.Errorf("updated fallback: %+v", updated.Fallback)
	}
}

func TestComboModels_NonAutoIgnoresTiers(t *testing.T) {
	s := openTemp(t)
	c := &model.TokenComboModel{
		TokenID: 1, Name: "plain", Models: []string{"m1"},
		Mode:    model.ComboModeLoadBalance,
		Enabled: true,
		Tiers:   map[string]model.TierConfig{"simple": {Models: []string{"m1"}}},
	}
	if err := s.CreateComboModel(c); err != nil {
		t.Fatalf("CreateComboModel: %v", err)
	}
	got, _ := s.GetComboModel(c.ID)
	if len(got.Tiers) != 1 {
		t.Errorf("load_balance combo should still round-trip tiers: %+v", got.Tiers)
	}
}

func TestComboModels_AutoValidation(t *testing.T) {
	s := openTemp(t)

	valid := &model.TokenComboModel{
		TokenID: 1, Name: "auto-ok", Mode: model.ComboModeAuto,
		Tiers:    map[string]model.TierConfig{"simple": {Models: []string{"m1"}}},
		Fallback: []string{"m2"},
	}
	if err := s.validateCombo(valid); err != nil {
		t.Errorf("valid auto combo rejected: %v", err)
	}

	tests := []struct {
		name    string
		combo   *model.TokenComboModel
		wantErr bool
	}{
		{
			"no tiers",
			&model.TokenComboModel{TokenID: 1, Name: "a", Mode: model.ComboModeAuto},
			true,
		},
		{
			"unknown tier name",
			&model.TokenComboModel{TokenID: 1, Name: "b", Mode: model.ComboModeAuto,
				Tiers: map[string]model.TierConfig{"ultra": {Models: []string{"m1"}}}},
			true,
		},
		{
			"empty tier models",
			&model.TokenComboModel{TokenID: 1, Name: "c", Mode: model.ComboModeAuto,
				Tiers: map[string]model.TierConfig{"simple": {Models: nil}}},
			true,
		},
		{
			"bad tier model chars",
			&model.TokenComboModel{TokenID: 1, Name: "d", Mode: model.ComboModeAuto,
				Tiers: map[string]model.TierConfig{"simple": {Models: []string{"bad model!"}}}},
			true,
		},
		{
			"bad fallback chars",
			&model.TokenComboModel{TokenID: 1, Name: "e", Mode: model.ComboModeAuto,
				Tiers:    map[string]model.TierConfig{"simple": {Models: []string{"m1"}}},
				Fallback: []string{"bad model!"}},
			true,
		},
		{
			"empty models list allowed for auto",
			&model.TokenComboModel{TokenID: 1, Name: "f", Mode: model.ComboModeAuto,
				Tiers: map[string]model.TierConfig{"simple": {Models: []string{"m1"}}}},
			false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := s.validateCombo(tc.combo)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateCombo() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestComboModels_CreateAuto_InvalidRejected(t *testing.T) {
	s := openTemp(t)
	c := &model.TokenComboModel{
		TokenID: 1, Name: "auto-bad", Mode: model.ComboModeAuto,
		Tiers: map[string]model.TierConfig{"nope": {Models: []string{"m1"}}},
	}
	err := s.CreateComboModel(c)
	if err == nil {
		t.Fatal("expected CreateComboModel to reject an unknown tier")
	}
	if _, gerr := s.GetComboModel(c.ID); !errors.Is(gerr, ErrNotFound) {
		t.Errorf("invalid combo must not be persisted, got %v", gerr)
	}
}
