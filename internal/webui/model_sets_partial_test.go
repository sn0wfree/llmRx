package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestModelSetsListPartial_Renders exercises the HTMX partial path
// that was at 0% coverage before this PR.
func TestModelSetsListPartial_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	sess := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")
	newCombo(t, st, tk.ID, "alpha")

	req := httptest.NewRequest(http.MethodGet, "/model-sets/partial/list", nil)
	req.Header.Set("HX-Request", "true")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	// The /partial/list endpoint should respond. Even when not
	// detected as HTMX, the handler renders the table body.
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !contains(body, "alpha") {
		t.Error("partial body should contain combo name")
	}
}

// TestModelSetsListPartial_QueryFilter exercises the search
// parameter on the partial path.
func TestModelSetsListPartial_QueryFilter(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	sess := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")
	newCombo(t, st, tk.ID, "alpha")
	newCombo(t, st, tk.ID, "bravo")

	req := httptest.NewRequest(http.MethodGet, "/model-sets/partial/list?q=alp", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	body := rec.Body.String()
	if !contains(body, "alpha") {
		t.Error("should contain alpha")
	}
	if contains(body, "bravo") {
		t.Error("should NOT contain bravo (filtered by q=alp)")
	}
}

// TestModelSetsListPartial_EnabledFilter verifies the
// yes/no/all filter on the partial path.
func TestModelSetsListPartial_EnabledFilter(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	sess := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")
	enabledCombo := newCombo(t, st, tk.ID, "enabled-one")
	disabledCombo := newCombo(t, st, tk.ID, "disabled-one")
	disabledCombo.Enabled = false
	if err := st.UpdateComboModel(disabledCombo); err != nil {
		t.Fatalf("disable: %v", err)
	}
	_ = enabledCombo

	req := httptest.NewRequest(http.MethodGet, "/model-sets/partial/list?enabled=yes", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	body := rec.Body.String()
	if contains(body, "disabled-one") {
		t.Error("should NOT contain disabled combo when filter=enabled=yes")
	}
}

// TestModelSetDetailPage_NotFound covers the 404 path.
func TestModelSetDetailPage_NotFound(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	sess := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/model-sets/999999", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

// TestModelSetDetailPage_AllDisabledChannels marks every channel
// disabled to verify the "no enabled channel" warning renders.
func TestModelSetDetailPage_AllDisabledChannels(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	sess := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")
	c := newCombo(t, st, tk.ID, "alpha")

	ch := modelChannelFixture()
	ch.Models = []string{"alpha"}
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	ch.Status = 1 // disabled (1 = disabled in the codebase per ChannelEnabled=0)
	if err := st.UpdateChannel(ch); err != nil {
		t.Fatalf("disable channel: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/model-sets/"+itoa(c.ID), nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	body := rec.Body.String()
	if !contains(body, "没有启用通道支持此模型") {
		t.Error("expected warning when all channels disabled")
	}
}