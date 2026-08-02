package webui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func newComboToken(t *testing.T, st interface {
	CreateToken(*model.Token) error
}, name string) *model.Token {
	t.Helper()
	tok := &model.Token{
		Key: "sk-combo-test-" + name, Name: name, Status: model.TokenActive,
	}
	if err := st.CreateToken(tok); err != nil {
		t.Fatalf("create token: %v", err)
	}
	return tok
}

func newCombo(t *testing.T, st interface {
	CreateComboModel(*model.TokenComboModel) error
}, tokenID int64, name string) *model.TokenComboModel {
	t.Helper()
	c := &model.TokenComboModel{
		TokenID: tokenID,
		Name:    name,
		Models:  []string{"m1", "m2"},
		Mode:    model.ComboModeLoadBalance,
		Enabled: true,
	}
	if err := st.CreateComboModel(c); err != nil {
		t.Fatalf("create combo: %v", err)
	}
	return c
}

func TestCombosPage_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")
	newCombo(t, st, tk.ID, "c1")

	req := httptest.NewRequest(http.MethodGet, "/tokens/"+itoa(tk.ID)+"/combos", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "c1") {
		t.Errorf("body should contain combo name 'c1'")
	}
}

func TestCombosPage_BadID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/tokens/abc/combos", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestCombosPage_NotFound(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/tokens/99999/combos", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestComboNewForm_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")

	req := httptest.NewRequest(http.MethodGet, "/tokens/"+itoa(tk.ID)+"/combos/new", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestComboNewForm_BadID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/tokens/abc/combos/new", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestComboEditForm_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")
	c := newCombo(t, st, tk.ID, "c1")

	req := httptest.NewRequest(http.MethodGet, "/tokens/"+itoa(tk.ID)+"/combos/"+itoa(c.ID)+"/edit", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestComboEditForm_BadTokenID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/tokens/abc/combos/1/edit", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestComboEditForm_BadComboID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")

	req := httptest.NewRequest(http.MethodGet, "/tokens/"+itoa(tk.ID)+"/combos/abc/edit", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestComboEditForm_TokenNotFound(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/tokens/99999/combos/1/edit", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestComboEditForm_ComboNotFound(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")

	req := httptest.NewRequest(http.MethodGet, "/tokens/"+itoa(tk.ID)+"/combos/99999/edit", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestComboCreate_Success(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")

	body := "name=smart-1&models=gpt-4o%0Aclaude-3&mode=load_balance&strategy=cheapest&status=1"
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	combos, _ := st.GetComboModels(tk.ID)
	if len(combos) != 1 || combos[0].Name != "smart-1" {
		t.Fatalf("expected 1 combo named smart-1, got %+v", combos)
	}
	if combos[0].Mode != model.ComboModeLoadBalance {
		t.Errorf("mode: got %q", combos[0].Mode)
	}
	if combos[0].Strategy != model.StrategyCheapest {
		t.Errorf("strategy: got %q", combos[0].Strategy)
	}
	if len(combos[0].Models) != 2 {
		t.Errorf("models: got %v", combos[0].Models)
	}
	if !combos[0].Enabled {
		t.Error("combo should be enabled")
	}
}

func TestComboCreate_BadTokenID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	body := "name=c1&models=m1"
	req := httptest.NewRequest(http.MethodPost, "/tokens/abc/combos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestComboCreate_MissingName(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")

	body := "name=&models=m1"
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	// New UX: missing name renders the form back with an error
	// banner (200) instead of a bare 400.
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", rec.Code)
	}
	if !contains(rec.Body.String(), "请修正表单错误") {
		t.Logf("body: %s", rec.Body.String())
		t.Error("expected form error banner in body")
	}
}

func TestComboCreate_MissingModels(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")

	body := "name=c1&models="
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", rec.Code)
	}
	if !contains(rec.Body.String(), "请修正表单错误") {
		t.Error("expected form error banner in body")
	}
}

func TestComboCreate_DefaultMode(t *testing.T) {
	// Empty mode should default to load_balance.
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")

	body := "name=c1&models=m1&mode="
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d", rec.Code)
	}
	combos, _ := st.GetComboModels(tk.ID)
	if len(combos) != 1 || combos[0].Mode != model.ComboModeLoadBalance {
		t.Fatalf("default mode not applied: got %+v", combos)
	}
}

func TestComboAction_Update(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")
	c := newCombo(t, st, tk.ID, "c1")

	body := "_method=PUT&name=renamed&models=m1%0Am2%0Am3&mode=serial&strategy=fastest&status=1"
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos/"+itoa(c.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	updated, _ := st.GetComboModel(c.ID)
	if updated.Name != "renamed" {
		t.Errorf("name: got %q", updated.Name)
	}
	if updated.Mode != model.ComboModeSerial {
		t.Errorf("mode: got %q", updated.Mode)
	}
	if updated.Strategy != model.StrategyFastest {
		t.Errorf("strategy: got %q", updated.Strategy)
	}
	if len(updated.Models) != 3 {
		t.Errorf("models len: got %d", len(updated.Models))
	}
}

func TestComboAction_UpdateBadTokenID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")
	c := newCombo(t, st, tk.ID, "c1")

	body := "_method=PUT&name=renamed"
	req := httptest.NewRequest(http.MethodPost, "/tokens/abc/combos/"+itoa(c.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestComboAction_UpdateBadComboID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")

	body := "_method=PUT&name=renamed"
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos/abc", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestComboAction_UpdateComboNotFound(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")

	body := "_method=PUT&name=renamed"
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos/99999", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestComboAction_BadMethod(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")
	c := newCombo(t, st, tk.ID, "c1")

	body := "_method=PATCH"
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos/"+itoa(c.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code=%d want 405", rec.Code)
	}
}

func TestComboAction_DefaultMethodIsPUT(t *testing.T) {
	// Empty _method defaults to PUT.
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")
	c := newCombo(t, st, tk.ID, "c1")

	body := "_method=&name=renamed"
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos/"+itoa(c.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d", rec.Code)
	}
	updated, _ := st.GetComboModel(c.ID)
	if updated.Name != "renamed" {
		t.Errorf("expected PUT to run with empty _method, got name=%q", updated.Name)
	}
}

func TestComboAction_DeleteViaForm(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")
	c := newCombo(t, st, tk.ID, "c1")

	body := "_method=DELETE"
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos/"+itoa(c.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	combos, _ := st.GetComboModels(tk.ID)
	if len(combos) != 0 {
		t.Errorf("combo should be deleted, got %+v", combos)
	}
}

func TestComboDelete_ViaDELETE(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")
	c := newCombo(t, st, tk.ID, "c1")

	req := httptest.NewRequest(http.MethodDelete, "/tokens/"+itoa(tk.ID)+"/combos/"+itoa(c.ID), nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	combos, _ := st.GetComboModels(tk.ID)
	if len(combos) != 0 {
		t.Errorf("combo should be deleted")
	}
}

func TestComboDelete_BadTokenID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodDelete, "/tokens/abc/combos/1", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestComboDelete_BadComboID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")

	req := httptest.NewRequest(http.MethodDelete, "/tokens/"+itoa(tk.ID)+"/combos/abc", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestComboUpdate_PartialFields(t *testing.T) {
	// Empty name + non-empty models → name preserved, models updated.
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")
	c := newCombo(t, st, tk.ID, "original-name")

	body := "_method=PUT&name=&models=mX%0AmY&mode=serial"
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos/"+itoa(c.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d", rec.Code)
	}
	updated, _ := st.GetComboModel(c.ID)
	if updated.Name != "original-name" {
		t.Errorf("name should be preserved when empty: got %q", updated.Name)
	}
	if len(updated.Models) != 2 || updated.Models[0] != "mX" {
		t.Errorf("models not updated: got %v", updated.Models)
	}
	if updated.Mode != model.ComboModeSerial {
		t.Errorf("mode not updated: got %q", updated.Mode)
	}
}

func TestComboSetDefault_Success(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	sess := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")
	c2 := newCombo(t, st, tk.ID, "bravo")

	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos/"+itoa(c2.ID)+"/set-default", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	combos, _ := st.GetComboModels(tk.ID)
	defaults := 0
	var chosen string
	for _, c := range combos {
		if c.IsDefault {
			defaults++
			chosen = c.Name
		}
	}
	if defaults != 1 || chosen != "bravo" {
		t.Errorf("expected 1 default named bravo, got %d default with chosen=%q", defaults, chosen)
	}
}

func TestComboSetDefault_BadTokenID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	sess := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodPost, "/tokens/abc/combos/1/set-default", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestComboSetDefault_BadComboID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	sess := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")

	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos/abc/set-default", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestComboCreate_AutoModeWithTiers(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk-auto")

	tiers := `{"simple":{"models":["deepseek-chat","gpt-4o-mini"]},"standard":{"models":["deepseek-chat","gpt-4o"]},"complex":{"models":["gpt-4o","claude-3-5-sonnet"]},"agentic":{"models":["claude-3-5-sonnet"]}}`
	body := "name=smart-auto&mode=auto&models=&strategy=&tiers=" + url.QueryEscape(tiers) + "&fallback=deepseek-chat&status=1"
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	combos, _ := st.GetComboModels(tk.ID)
	if len(combos) != 1 {
		t.Fatalf("expected 1 combo, got %d", len(combos))
	}
	c := combos[0]
	if c.Mode != model.ComboModeAuto {
		t.Fatalf("mode = %q, want auto", c.Mode)
	}
	if len(c.Tiers) != 4 {
		t.Fatalf("tiers = %d entries, want 4: %+v", len(c.Tiers), c.Tiers)
	}
	if c.Tiers["simple"].Models[0] != "deepseek-chat" || c.Tiers["agentic"].Models[0] != "claude-3-5-sonnet" {
		t.Errorf("tiers content wrong: %+v", c.Tiers)
	}
	if len(c.Fallback) != 1 || c.Fallback[0] != "deepseek-chat" {
		t.Errorf("fallback: %+v", c.Fallback)
	}
}

func TestComboCreate_AutoModeRequiresTiers(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk-auto-bad")

	body := "name=bad-auto&mode=auto&models=m1&tiers=&fallback="
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200 (form re-render)", rec.Code)
	}
	if !contains(rec.Body.String(), "auto 模式需要复杂度分档配置") {
		t.Errorf("expected tiers validation error, got: %s", rec.Body.String())
	}
	combos, _ := st.GetComboModels(tk.ID)
	if len(combos) != 0 {
		t.Fatal("invalid auto combo must not be persisted")
	}
}

func TestComboCreate_AutoModeBadTiersJSON(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk-auto-badjson")

	body := "name=badjson-auto&mode=auto&models=&tiers=" + url.QueryEscape("{not json}") + "&fallback="
	req := httptest.NewRequest(http.MethodPost, "/tokens/"+itoa(tk.ID)+"/combos", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200 (form re-render)", rec.Code)
	}
	if !contains(rec.Body.String(), "tiers JSON 解析失败") {
		t.Errorf("expected tiers JSON error, got: %s", rec.Body.String())
	}
}
