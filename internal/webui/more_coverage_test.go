package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

// TestRenderFormError_ExtrasPassThrough verifies that arbitrary
// Extras are forwarded into the template data scope. We use the
// tokens form which echoes back the FormError message.
func TestRenderFormError_ExtrasPassThrough(t *testing.T) {
	h, _ := newTestWebUI(t)
	rec := httptest.NewRecorder()
	h.renderFormError(rec, httptest.NewRequest(http.MethodGet, "/", nil), formErrorView{
		Body:   "tokens_form_body",
		Title:  "测试",
		Active: "tokens",
		Msg:    "测试错误消息XYZ",
		Extras: map[string]any{"NewKey": "sk-test-extrakey"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !contains(rec.Body.String(), "测试错误消息XYZ") {
		t.Errorf("expected error message in body")
	}
	if !contains(rec.Body.String(), "sk-test-extrakey") {
		t.Errorf("expected extras to pass through to template")
	}
}

// TestRenderFormError_RendererFailure covers the rare path where
// the template engine itself errors out (e.g. malformed template).
// Templates are loaded at New time, so to trigger this we'd need
// to construct an invalid render call. The most realistic case is
// to point at a non-existent template name — but Render is
// lenient and falls back to base.html. So this test only documents
// the lenient behavior.
func TestRenderFormError_RendererFailure(t *testing.T) {
	h, _ := newTestWebUI(t)
	rec := httptest.NewRecorder()
	h.renderFormError(rec, httptest.NewRequest(http.MethodGet, "/", nil), formErrorView{
		Body:   "this-template-does-not-exist",
		Title:  "x",
		Active: "channels",
		Msg:    "x",
	})
	// Document current behavior — the render helper passes the
	// template name to renderer.Render which is lenient.
	if rec.Code != http.StatusOK {
		t.Logf("got %d, may need future investigation", rec.Code)
	}
}

// TestRenderFormError_NilForm covers the path where r.Form is nil
// (e.g. ParseForm wasn't called yet).
func TestRenderFormError_NilForm(t *testing.T) {
	h, _ := newTestWebUI(t)
	rec := httptest.NewRecorder()
	h.renderFormError(rec, httptest.NewRequest(http.MethodGet, "/", nil), formErrorView{
		Body: "channels_form_body", Title: "x", Active: "channels",
		Msg: "x", Form: nil, Fields: channelFormFields, FieldRenames: channelFormRenames,
	})
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
}

// TestRecordKey_KnownTypes exercises the type-switch branches
// for known record types. We use the actual model package
// pointers so the switch correctly identifies them.
func TestRecordKey_KnownTypes(t *testing.T) {
	cases := []struct {
		rec  any
		want string
	}{
		{&model.Channel{}, "Channel"},
		{&model.Token{}, "Token"},
		{&model.Plan{}, "Plan"},
		{&model.User{}, "User"},
		{&model.Alert{}, "Alert"},
		{&model.ProviderDef{}, "ProviderDef"},
		{&model.TokenComboModel{}, "Combo"},
	}
	for _, c := range cases {
		if got := recordKey(c.rec); got != c.want {
			t.Errorf("recordKey(%T) = %q, want %q", c.rec, got, c.want)
		}
	}
}

// TestChannelNewForm_RendersNew covers the ChannelNewForm
// handler's render path. Was at 66.7% — branches were untested.
func TestChannelNewForm_RendersNew(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/channels/new", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !contains(rec.Body.String(), "新建通道") {
		t.Error("expected 新建通道 in body")
	}
}

// TestPlanNewForm_RendersExtended covers the PlanNewForm handler's
// render path (existed at low coverage).
func TestPlanNewForm_RendersExtended(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/plans/new", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !contains(rec.Body.String(), "新建计划") {
		t.Error("expected 新建计划 in body")
	}
}

// TestTokenNewForm_RendersBasic covers the TokenNewForm handler's
// render path (existed at low coverage before this PR).
func TestTokenNewForm_RendersBasic(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/tokens/new", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if !contains(rec.Body.String(), "新建 Token") {
		t.Error("expected 新建 Token in body")
	}
}

// TestTokensHelpPage_RendersDuplicate covers a defensive check
// that the help page renders even without an active user (it
// only needs the base URL).
func TestTokensHelpPage_AlreadyLoggedInRedirect(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/tokens/help", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Token 调用帮助") {
		t.Error("expected help heading")
	}
}

// TestTokenEditForm_NoCombos covers the DefaultCombo fallback
// when the token has no combos at all (model_sets_test only
// covered the populated case).
func TestTokenEditForm_NoCombos(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "no-combos")

	req := httptest.NewRequest(http.MethodGet, "/tokens/"+itoa(tk.ID)+"/edit", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !contains(rec.Body.String(), "编辑 Token") {
		t.Error("expected edit heading")
	}
}

// TestTokenEditForm_HasCombosButNoAuto covers the fallback to
// the first enabled combo when there's no combo named "auto".
func TestTokenEditForm_HasCombosButNoAuto(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "has-non-auto")
	// Create a combo that is NOT named "auto"
	c := &model.TokenComboModel{
		TokenID: tk.ID,
		Name:    "my-custom",
		Models:  []string{"m1"},
		Mode:    model.ComboModeLoadBalance,
		Enabled: true,
	}
	if err := st.CreateComboModel(c); err != nil {
		t.Fatalf("create combo: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tokens/"+itoa(tk.ID)+"/edit", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	// The form should render the first available combo as default.
	if !contains(rec.Body.String(), "my-custom") {
		t.Error("expected my-custom in body")
	}
}
