package store

import (
	"errors"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestComboModels_CRUD(t *testing.T) {
	s := openTemp(t)

	// Create
	c := &model.TokenComboModel{
		TokenID:  1,
		Name:     "smart-1",
		Models:   []string{"gpt-4o", "claude-3-5-sonnet"},
		Mode:     model.ComboModeLoadBalance,
		Strategy: "",
		Enabled:  true,
	}
	if err := s.CreateComboModel(c); err != nil {
		t.Fatalf("CreateComboModel: %v", err)
	}
	if c.ID == 0 {
		t.Fatal("expected non-zero ID after create")
	}

	// Get by ID
	got, err := s.GetComboModel(c.ID)
	if err != nil {
		t.Fatalf("GetComboModel: %v", err)
	}
	if got.Name != "smart-1" || got.TokenID != 1 || len(got.Models) != 2 {
		t.Errorf("GetComboModel: got %+v", got)
	}
	if got.Mode != model.ComboModeLoadBalance {
		t.Errorf("mode: got %q, want load_balance", got.Mode)
	}
	if !got.Enabled {
		t.Error("expected enabled=true")
	}

	// Get by token
	combos, err := s.GetComboModels(1)
	if err != nil {
		t.Fatalf("GetComboModels: %v", err)
	}
	if len(combos) != 1 {
		t.Fatalf("expected 1 combo, got %d", len(combos))
	}

	// Update
	got.Strategy = model.StrategyCheapest
	got.Models = []string{"gpt-4o", "claude-3-5-sonnet", "gemini-2.5-flash"}
	if err := s.UpdateComboModel(got); err != nil {
		t.Fatalf("UpdateComboModel: %v", err)
	}
	updated, _ := s.GetComboModel(c.ID)
	if updated.Strategy != model.StrategyCheapest {
		t.Errorf("strategy: got %q, want cheapest", updated.Strategy)
	}
	if len(updated.Models) != 3 {
		t.Errorf("models len: got %d, want 3", len(updated.Models))
	}

	// Delete
	if err := s.DeleteComboModel(c.ID); err != nil {
		t.Fatalf("DeleteComboModel: %v", err)
	}
	if _, err := s.GetComboModel(c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestComboModels_GetAllComboModels(t *testing.T) {
	s := openTemp(t)

	// Create combos for two different tokens
	s.CreateComboModel(&model.TokenComboModel{
		TokenID: 1, Name: "a1", Models: []string{"m1"}, Mode: model.ComboModeLoadBalance, Enabled: true,
	})
	s.CreateComboModel(&model.TokenComboModel{
		TokenID: 2, Name: "b1", Models: []string{"m2"}, Mode: model.ComboModeSerial, Enabled: true,
	})
	// Disabled combo should not appear
	s.CreateComboModel(&model.TokenComboModel{
		TokenID: 1, Name: "disabled", Models: []string{"m3"}, Mode: model.ComboModeLoadBalance, Enabled: false,
	})

	all, err := s.GetAllComboModels()
	if err != nil {
		t.Fatalf("GetAllComboModels: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 enabled combos, got %d", len(all))
	}
}

func TestComboModels_NameConflict(t *testing.T) {
	s := openTemp(t)

	// Create a channel with model "gpt-4o"
	s.CreateChannel(&model.Channel{
		Name: "openai", Provider: "openai", BaseURL: "https://api.openai.com/v1",
		Models: []string{"gpt-4o"}, Status: model.ChannelEnabled,
	})

	// Creating a combo with the same name should fail
	err := s.CreateComboModel(&model.TokenComboModel{
		TokenID: 1, Name: "gpt-4o", Models: []string{"m"}, Mode: model.ComboModeLoadBalance, Enabled: true,
	})
	if err == nil {
		t.Fatal("expected error for combo name conflict with real model")
	}
}

func TestComboModels_Validation(t *testing.T) {
	s := openTemp(t)

	tests := []struct {
		name    string
		combo   model.TokenComboModel
		wantErr bool
	}{
		{"empty name", model.TokenComboModel{TokenID: 1, Name: "", Models: []string{"m"}, Mode: model.ComboModeLoadBalance}, true},
		{"bad name chars", model.TokenComboModel{TokenID: 1, Name: "bad name!", Models: []string{"m"}, Mode: model.ComboModeLoadBalance}, true},
		{"name too long", model.TokenComboModel{TokenID: 1, Name: "a]65", Models: []string{"m"}, Mode: model.ComboModeLoadBalance}, true}, // placeholder, tested below
		{"empty models", model.TokenComboModel{TokenID: 1, Name: "ok", Models: nil, Mode: model.ComboModeLoadBalance}, true},
		{"bad model chars", model.TokenComboModel{TokenID: 1, Name: "ok", Models: []string{"bad model!"}, Mode: model.ComboModeLoadBalance}, true},
		{"bad mode", model.TokenComboModel{TokenID: 1, Name: "ok", Models: []string{"m"}, Mode: "parallel"}, true},
		{"bad strategy", model.TokenComboModel{TokenID: 1, Name: "ok", Models: []string{"m"}, Mode: model.ComboModeLoadBalance, Strategy: "random"}, true},
		{"valid load_balance", model.TokenComboModel{TokenID: 1, Name: "valid-1", Models: []string{"m"}, Mode: model.ComboModeLoadBalance}, false},
		{"valid serial", model.TokenComboModel{TokenID: 1, Name: "valid-2", Models: []string{"m"}, Mode: model.ComboModeSerial, Strategy: model.StrategyCheapest}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := s.validateCombo(&tc.combo)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateCombo() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestComboModels_NameTooLong(t *testing.T) {
	s := openTemp(t)
	long := ""
	for i := 0; i < 65; i++ {
		long += "a"
	}
	err := s.validateCombo(&model.TokenComboModel{
		TokenID: 1, Name: long, Models: []string{"m"}, Mode: model.ComboModeLoadBalance,
	})
	if err == nil {
		t.Fatal("expected error for name > 64 chars")
	}
}

func TestComboModels_TooManyModels(t *testing.T) {
	s := openTemp(t)
	models := make([]string, 101)
	for i := range models {
		models[i] = "m"
	}
	err := s.validateCombo(&model.TokenComboModel{
		TokenID: 1, Name: "ok", Models: models, Mode: model.ComboModeLoadBalance,
	})
	if err == nil {
		t.Fatal("expected error for > 100 models")
	}
}

func TestComboModels_GetComboModel_NotFound(t *testing.T) {
	s := openTemp(t)
	_, err := s.GetComboModel(999)
	if err == nil {
		t.Fatal("expected error for non-existent combo")
	}
}

func TestComboModels_DeleteComboModel_NotFound(t *testing.T) {
	s := openTemp(t)
	if err := s.DeleteComboModel(999); err != nil {
		t.Errorf("delete non-existent should not error, got %v", err)
	}
}

func TestComboModels_SetDefaultModelSet(t *testing.T) {
	s := openTemp(t)
	tok := &model.Token{Key: "sk-test", Name: "t1", Status: model.TokenActive}
	if err := s.CreateToken(tok); err != nil {
		t.Fatalf("create token: %v", err)
	}
	c1 := &model.TokenComboModel{TokenID: tok.ID, Name: "alpha", Models: []string{"m1"}, Mode: model.ComboModeLoadBalance, Strategy: model.StrategyBalanced, Enabled: true}
	c2 := &model.TokenComboModel{TokenID: tok.ID, Name: "bravo", Models: []string{"m2"}, Mode: model.ComboModeLoadBalance, Strategy: model.StrategyBalanced, Enabled: true}
	c3 := &model.TokenComboModel{TokenID: tok.ID, Name: "charlie", Models: []string{"m3"}, Mode: model.ComboModeLoadBalance, Strategy: model.StrategyBalanced, Enabled: true}
	for _, c := range []*model.TokenComboModel{c1, c2, c3} {
		if err := s.CreateComboModel(c); err != nil {
			t.Fatalf("create %s: %v", c.Name, err)
		}
	}

	if err := s.SetDefaultModelSet(tok.ID, c2.ID); err != nil {
		t.Fatalf("set default: %v", err)
	}
	combos, _ := s.GetComboModels(tok.ID)
	defaults := 0
	var chosen string
	for _, c := range combos {
		if c.IsDefault {
			defaults++
			chosen = c.Name
		}
	}
	if defaults != 1 {
		t.Fatalf("expected exactly 1 default, got %d", defaults)
	}
	if chosen != "bravo" {
		t.Errorf("default: got %q, want bravo", chosen)
	}

	if err := s.SetDefaultModelSet(tok.ID, c3.ID); err != nil {
		t.Fatalf("set default to charlie: %v", err)
	}
	combos, _ = s.GetComboModels(tok.ID)
	defaults = 0
	chosen = ""
	for _, c := range combos {
		if c.IsDefault {
			defaults++
			chosen = c.Name
		}
	}
	if defaults != 1 || chosen != "charlie" {
		t.Errorf("after switch: defaults=%d chosen=%q", defaults, chosen)
	}

	if err := s.SetDefaultModelSet(tok.ID, 0); err != nil {
		t.Fatalf("clear default: %v", err)
	}
	combos, _ = s.GetComboModels(tok.ID)
	for _, c := range combos {
		if c.IsDefault {
			t.Errorf("after clear: %s should not be default", c.Name)
		}
	}
}

func TestComboModels_DefaultFlag_RoundTrip(t *testing.T) {
	s := openTemp(t)
	tok := &model.Token{Key: "sk-test", Name: "t1", Status: model.TokenActive}
	if err := s.CreateToken(tok); err != nil {
		t.Fatalf("create token: %v", err)
	}
	c := &model.TokenComboModel{TokenID: tok.ID, Name: "default-set", Models: []string{"m1"}, Mode: model.ComboModeLoadBalance, Strategy: model.StrategyBalanced, Enabled: true, IsDefault: true}
	if err := s.CreateComboModel(c); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetComboModel(c.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.IsDefault {
		t.Error("IsDefault should persist as true")
	}
}

func TestComboModels_DefaultFlag_ClearedOnCreate(t *testing.T) {
	s := openTemp(t)
	tok := &model.Token{Key: "sk-test", Name: "t1", Status: model.TokenActive}
	if err := s.CreateToken(tok); err != nil {
		t.Fatalf("create token: %v", err)
	}
	c1 := &model.TokenComboModel{TokenID: tok.ID, Name: "set-a", Models: []string{"m1"}, Mode: model.ComboModeLoadBalance, Strategy: model.StrategyBalanced, Enabled: true, IsDefault: true}
	if err := s.CreateComboModel(c1); err != nil {
		t.Fatalf("create set-a: %v", err)
	}
	c2 := &model.TokenComboModel{TokenID: tok.ID, Name: "set-b", Models: []string{"m2"}, Mode: model.ComboModeLoadBalance, Strategy: model.StrategyBalanced, Enabled: true, IsDefault: true}
	if err := s.CreateComboModel(c2); err != nil {
		t.Fatalf("create set-b: %v", err)
	}
	combos, _ := s.GetComboModels(tok.ID)
	defaults := 0
	for _, c := range combos {
		if c.IsDefault {
			defaults++
		}
	}
	if defaults != 1 {
		t.Errorf("expected exactly 1 default after second insert, got %d", defaults)
	}
}
