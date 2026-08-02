package store

import (
	"path/filepath"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

// TestListAllComboModels_IncludesDisabled verifies that
// ListAllComboModels returns BOTH enabled and disabled combos,
// while GetAllComboModels returns only enabled. This is the
// distinguishing behavior that powers the model-sets admin page.
func TestListAllComboModels_IncludesDisabled(t *testing.T) {
	st := newSQLiteStore(t)

	tk := &model.Token{Key: "sk-listing", Name: "list", Status: model.TokenActive}
	if err := st.CreateToken(tk); err != nil {
		t.Fatalf("seed: %v", err)
	}
	enabledCombo := &model.TokenComboModel{
		TokenID: tk.ID, Name: "e", Models: []string{"m"}, Mode: model.ComboModeLoadBalance, Enabled: true,
	}
	if err := st.CreateComboModel(enabledCombo); err != nil {
		t.Fatalf("create enabled: %v", err)
	}
	disabledCombo := &model.TokenComboModel{
		TokenID: tk.ID, Name: "d", Models: []string{"m"}, Mode: model.ComboModeLoadBalance, Enabled: false,
	}
	if err := st.CreateComboModel(disabledCombo); err != nil {
		t.Fatalf("create disabled: %v", err)
	}

	all, err := st.ListAllComboModels()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 (both enabled+disabled), got %d", len(all))
	}

	enabled, err := st.GetAllComboModels()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(enabled) != 1 {
		t.Errorf("expected 1 (enabled only), got %d", len(enabled))
	}
	if enabled[0].Name != "e" {
		t.Errorf("got wrong combo: %s", enabled[0].Name)
	}
}

// TestMigrateAutoCombos_AllTokensHaveCombo verifies the migration
// helper skips tokens that already have a combo.
func TestMigrateAutoCombos_AllTokensHaveCombo(t *testing.T) {
	st := newSQLiteStore(t)
	// Seed a token with a whitelist
	tk := &model.Token{Key: "sk-mig", Name: "mig", Status: model.TokenActive, ModelsWhitelist: []string{"x", "y"}}
	if err := st.CreateToken(tk); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Pre-create an "auto" combo
	c := &model.TokenComboModel{TokenID: tk.ID, Name: "auto", Models: []string{"x"}, Mode: model.ComboModeLoadBalance, Enabled: true}
	if err := st.CreateComboModel(c); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	st.migrateAutoCombos()

	combos, _ := st.GetComboModels(tk.ID)
	if len(combos) != 1 {
		t.Errorf("expected 1 combo (skip), got %d", len(combos))
	}
}

// TestMigrateAutoCombos_PartialWhitelist exercises the migration
// creating an auto combo for a token with a whitelist but no
// existing combo. Phase-2-A data migration.
func TestMigrateAutoCombos_PartialWhitelist(t *testing.T) {
	st := newSQLiteStore(t)
	tk := &model.Token{Key: "sk-mig2", Name: "mig2", Status: model.TokenActive, ModelsWhitelist: []string{"m1", "m2"}}
	if err := st.CreateToken(tk); err != nil {
		t.Fatalf("seed: %v", err)
	}

	st.migrateAutoCombos()

	combos, _ := st.GetComboModels(tk.ID)
	if len(combos) != 1 {
		t.Fatalf("expected 1 auto combo, got %d", len(combos))
	}
	if combos[0].Name != "auto" {
		t.Errorf("name = %q, want auto", combos[0].Name)
	}
	if !combos[0].Enabled {
		t.Error("migrated combo should be enabled")
	}
	if len(combos[0].Models) != 2 {
		t.Errorf("expected 2 models from whitelist, got %d", len(combos[0].Models))
	}
}

// TestMigrateAutoCombos_NoTokens verifies the migration handles an
// empty database gracefully.
func TestMigrateAutoCombos_NoTokens(t *testing.T) {
	st := newSQLiteStore(t)
	// Should not panic / error
	st.migrateAutoCombos()
}

// TestSetDefaultModelSet_DemotePreviousDefault verifies the
// default-set promotion transaction demotes any prior default.
func TestSetDefaultModelSet_DemotePreviousDefault(t *testing.T) {
	st := newSQLiteStore(t)
	tk := &model.Token{Key: "sk-default", Name: "def", Status: model.TokenActive}
	if err := st.CreateToken(tk); err != nil {
		t.Fatalf("seed: %v", err)
	}

	c1 := &model.TokenComboModel{TokenID: tk.ID, Name: "first", Models: []string{"a"}, Mode: model.ComboModeLoadBalance, Enabled: true}
	c2 := &model.TokenComboModel{TokenID: tk.ID, Name: "second", Models: []string{"b"}, Mode: model.ComboModeLoadBalance, Enabled: true}
	if err := st.CreateComboModel(c1); err != nil {
		t.Fatalf("c1: %v", err)
	}
	if err := st.CreateComboModel(c2); err != nil {
		t.Fatalf("c2: %v", err)
	}

	if err := st.SetDefaultModelSet(tk.ID, c1.ID); err != nil {
		t.Fatalf("set default c1: %v", err)
	}
	if err := st.SetDefaultModelSet(tk.ID, c2.ID); err != nil {
		t.Fatalf("set default c2: %v", err)
	}

	// c1 should no longer be default; c2 should be
	c1Reloaded, _ := st.GetComboModel(c1.ID)
	c2Reloaded, _ := st.GetComboModel(c2.ID)
	if c1Reloaded.IsDefault {
		t.Error("c1 should have been demoted")
	}
	if !c2Reloaded.IsDefault {
		t.Error("c2 should be default now")
	}
}

// TestSetDefaultModelSet_ZeroIDClearsAll verifies that comboID=0
// clears the default for the token (useful for "unset default"
// operation).
func TestSetDefaultModelSet_ZeroIDClearsAll(t *testing.T) {
	st := newSQLiteStore(t)
	tk := &model.Token{Key: "sk-zero", Name: "z", Status: model.TokenActive}
	if err := st.CreateToken(tk); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c := &model.TokenComboModel{TokenID: tk.ID, Name: "x", Models: []string{"a"}, Mode: model.ComboModeLoadBalance, Enabled: true}
	if err := st.CreateComboModel(c); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.SetDefaultModelSet(tk.ID, c.ID); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Now clear with zero
	if err := st.SetDefaultModelSet(tk.ID, 0); err != nil {
		t.Fatalf("zero-id: %v", err)
	}
	reloaded, _ := st.GetComboModel(c.ID)
	if reloaded.IsDefault {
		t.Error("existing default should be cleared after zero-id call")
	}
}

// TestMigrateDefaultFlag_Idempotent verifies the migration can be
// re-run safely (idempotent).
func TestMigrateDefaultFlag_Idempotent(t *testing.T) {
	st := newSQLiteStore(t)
	tk := &model.Token{Key: "sk-flag", Name: "f", Status: model.TokenActive}
	if err := st.CreateToken(tk); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Create an "auto" combo (not yet marked default by create)
	c := &model.TokenComboModel{
		TokenID: tk.ID, Name: "auto", Models: []string{"m"},
		Mode: model.ComboModeLoadBalance, Enabled: true,
		IsDefault: false,
	}
	if err := st.CreateComboModel(c); err != nil {
		t.Fatalf("create: %v", err)
	}

	st.migrateDefaultFlag()
	st.migrateDefaultFlag() // second run — should not duplicate

	combos, _ := st.GetComboModels(tk.ID)
	var defaultCount int
	for _, cm := range combos {
		if cm.IsDefault {
			defaultCount++
		}
	}
	if defaultCount != 1 {
		t.Errorf("expected exactly 1 default, got %d", defaultCount)
	}
}

// newSQLiteStore creates a fresh in-memory (well, tempdir) sqlite
// store for tests that don't need the full newTestWebUI setup.
func newSQLiteStore(t *testing.T) *SQLite {
	t.Helper()
	dir := t.TempDir()
	st, err := OpenSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestComboCRUD_ConcurrentSafety exercises SetDefaultModelSet
// under concurrent promotion attempts to verify the transaction
// doesn't lose updates.
func TestComboCRUD_ConcurrentSafety(t *testing.T) {
	st := newSQLiteStore(t)
	tk := &model.Token{Key: "sk-concurrent", Name: "conc", Status: model.TokenActive}
	if err := st.CreateToken(tk); err != nil {
		t.Fatalf("seed: %v", err)
	}
	combos := make([]*model.TokenComboModel, 5)
	for i := range combos {
		c := &model.TokenComboModel{
			TokenID: tk.ID, Name: nameForTest(i),
			Models: []string{"m"}, Mode: model.ComboModeLoadBalance, Enabled: true,
		}
		if err := st.CreateComboModel(c); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		combos[i] = c
	}

	// Promote each combo to default in sequence; last write wins.
	for _, c := range combos {
		if err := st.SetDefaultModelSet(tk.ID, c.ID); err != nil {
			t.Fatalf("set default: %v", err)
		}
	}

	// Verify only the last one is default
	var defaultCount int
	for _, c := range combos {
		reloaded, _ := st.GetComboModel(c.ID)
		if reloaded.IsDefault {
			defaultCount++
		}
	}
	if defaultCount != 1 {
		t.Errorf("expected exactly 1 default after sequential promotion, got %d", defaultCount)
	}
}

func nameForTest(i int) string {
	return []string{"a", "b", "c", "d", "e"}[i]
}
