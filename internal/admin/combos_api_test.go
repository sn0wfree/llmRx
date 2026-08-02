package admin_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// ──────────────────────────────────────────────────────────
// Combos API (admin JSON endpoints, not the webui HTML ones).
// ──────────────────────────────────────────────────────────

func TestAdmin_ListCombos(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	tk := app.AddToken("sk-combo-api", "tk-combo")
	app.AddComboModel(tk.Key, "smart-1", []string{"gpt-4o", "claude-3"}, model.ComboModeLoadBalance)

	rec := do(t, app.Admin.Routes(), http.MethodGet, "/tokens/"+itoa(tk.ID)+"/combos", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "smart-1") {
		t.Errorf("body should contain combo name: %s", rec.Body.String())
	}
}

func TestAdmin_ListCombos_BadID(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/tokens/abc/combos", sess, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_ListCombos_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.GetComboModelsFunc = func(tokenID int64) ([]model.TokenComboModel, error) {
		return nil, errTestStore
	}
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/tokens/1/combos", sess, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAdmin_CreateCombo_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	tk := app.AddToken("sk-new-combo", "tk-new")

	body := `{"name":"smart-2","models":["m1","m2"],"mode":"serial","strategy":"cheapest","enabled":true}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "smart-2") {
		t.Errorf("body should echo name: %s", rec.Body.String())
	}
}

func TestAdmin_CreateCombo_BadID(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	body := `{"name":"c","models":["m"]}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/tokens/abc/combos", sess, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_CreateCombo_BadBody(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	tk := app.AddToken("sk-bad-combo", "tk-bad")
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos", sess, "not json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_CreateCombo_MissingName(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	tk := app.AddToken("sk-noname-combo", "tk-noname")
	body := `{"name":"","models":["m"]}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos", sess, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_CreateCombo_EmptyModels(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	tk := app.AddToken("sk-nomodel-combo", "tk-nomodel")
	body := `{"name":"c","models":[]}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos", sess, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_CreateCombo_DefaultMode(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	tk := app.AddToken("sk-default-mode", "tk-default")
	body := `{"name":"c","models":["m"]}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	combos, _ := app.Store.GetComboModels(tk.ID)
	if len(combos) != 1 || combos[0].Mode != model.ComboModeLoadBalance {
		t.Fatalf("default mode not applied: %+v", combos)
	}
}

func TestAdmin_CreateCombo_DefaultEnabled(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	tk := app.AddToken("sk-default-enabled", "tk-de")
	body := `{"name":"c","models":["m"]}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	combos, _ := app.Store.GetComboModels(tk.ID)
	if !combos[0].Enabled {
		t.Errorf("enabled should default to true")
	}
}

func TestAdmin_CreateCombo_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	tk := app.AddToken("sk-combo-fail", "tk-cf")
	ss.CreateComboModelFunc = func(c *model.TokenComboModel) error {
		return errTestStore
	}
	body := `{"name":"c","models":["m"]}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos", sess, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_UpdateCombo_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	tk := app.AddToken("sk-update-combo", "tk-uc")
	app.AddComboModel(tk.Key, "original", []string{"m1"}, model.ComboModeLoadBalance)
	combos, _ := app.Store.GetComboModels(tk.ID)
	cid := combos[0].ID

	body := `{"name":"renamed","models":["x","y","z"],"mode":"serial","strategy":"cheapest","enabled":false}`
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/combos/"+itoa(cid), sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	updated, _ := app.Store.GetComboModel(cid)
	if updated.Name != "renamed" {
		t.Errorf("name: got %q", updated.Name)
	}
	if updated.Mode != model.ComboModeSerial {
		t.Errorf("mode: got %q", updated.Mode)
	}
	if updated.Strategy != model.StrategyCheapest {
		t.Errorf("strategy: got %q", updated.Strategy)
	}
	if len(updated.Models) != 3 {
		t.Errorf("models: %v", updated.Models)
	}
	if updated.Enabled {
		t.Error("expected disabled")
	}
}

func TestAdmin_UpdateCombo_BadID(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/combos/abc", sess, `{"name":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_UpdateCombo_NotFound(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/combos/999", sess, `{"name":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestAdmin_UpdateCombo_BadBody(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	tk := app.AddToken("sk-combo-bad-body", "tk-cbb")
	app.AddComboModel(tk.Key, "c", []string{"m"}, model.ComboModeLoadBalance)
	combos, _ := app.Store.GetComboModels(tk.ID)
	cid := combos[0].ID

	rec := do(t, app.Admin.Routes(), http.MethodPut, "/combos/"+itoa(cid), sess, "not json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_UpdateCombo_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	tk := app.AddToken("sk-combo-upd-err", "tk-cue")
	app.AddComboModel(tk.Key, "c", []string{"m"}, model.ComboModeLoadBalance)
	combos, _ := app.Store.GetComboModels(tk.ID)
	cid := combos[0].ID

	ss.UpdateComboModelFunc = func(c *model.TokenComboModel) error {
		return errTestStore
	}
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/combos/"+itoa(cid), sess, `{"name":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_UpdateCombo_PartialFields(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	tk := app.AddToken("sk-combo-partial", "tk-cp")
	app.AddComboModel(tk.Key, "original", []string{"m1", "m2"}, model.ComboModeLoadBalance)
	combos, _ := app.Store.GetComboModels(tk.ID)
	cid := combos[0].ID

	// Only update name; everything else should be preserved.
	body := `{"name":"renamed"}`
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/combos/"+itoa(cid), sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	updated, _ := app.Store.GetComboModel(cid)
	if updated.Name != "renamed" {
		t.Errorf("name: got %q", updated.Name)
	}
	if len(updated.Models) != 2 {
		t.Errorf("models should be preserved: got %v", updated.Models)
	}
	if updated.Mode != model.ComboModeLoadBalance {
		t.Errorf("mode should be preserved: got %q", updated.Mode)
	}
}

func TestAdmin_DeleteCombo_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	tk := app.AddToken("sk-combo-del", "tk-cd")
	app.AddComboModel(tk.Key, "c", []string{"m"}, model.ComboModeLoadBalance)
	combos, _ := app.Store.GetComboModels(tk.ID)
	cid := combos[0].ID

	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/combos/"+itoa(cid), sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	combos, _ = app.Store.GetComboModels(tk.ID)
	if len(combos) != 0 {
		t.Errorf("combo should be deleted")
	}
}

func TestAdmin_DeleteCombo_BadID(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/combos/abc", sess, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_DeleteCombo_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.DeleteComboModelFunc = func(id int64) error {
		return errTestStore
	}
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/combos/1", sess, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

// itoaForTest is intentionally omitted — handler_test.go already
// provides `itoa(int64) string` for the same purpose.
