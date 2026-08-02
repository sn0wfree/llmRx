package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestModelSetsPage_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	sess := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")
	newCombo(t, st, tk.ID, "alpha")

	req := httptest.NewRequest(http.MethodGet, "/model-sets", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !contains(rec.Body.String(), "alpha") {
		t.Error("body should contain combo name 'alpha'")
	}
}

func TestModelSetsPage_FilterByToken(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	sess := sessionCookieFor(t, st, admin)
	tk1 := newComboToken(t, st, "tk1")
	tk2 := newComboToken(t, st, "tk2")
	newCombo(t, st, tk1.ID, "alpha")
	newCombo(t, st, tk2.ID, "bravo")

	req := httptest.NewRequest(http.MethodGet, "/model-sets?token_id="+itoa(tk1.ID), nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	body := rec.Body.String()
	if !contains(body, "alpha") {
		t.Error("should contain tk1's combo")
	}
	if contains(body, "bravo") {
		t.Error("should NOT contain tk2's combo")
	}
}

func TestModelSetsPage_FilterByEnabled(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	sess := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")
	newCombo(t, st, tk.ID, "enabled-set")
	disabled := newCombo(t, st, tk.ID, "disabled-set")
	disabled.Enabled = false
	if err := st.UpdateComboModel(disabled); err != nil {
		t.Fatalf("disable: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/model-sets?enabled=yes", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	body := rec.Body.String()
	if !contains(body, "enabled-set") {
		t.Error("should contain enabled combo")
	}
	if contains(body, "disabled-set") {
		t.Error("should NOT contain disabled combo")
	}

	req2 := httptest.NewRequest(http.MethodGet, "/model-sets?enabled=no", nil)
	req2.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec2 := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("code=%d", rec2.Code)
	}
	body = rec2.Body.String()
	if contains(body, "enabled-set") {
		t.Error("enabled filter=no should NOT contain enabled combo")
	}
	if !contains(body, "disabled-set") {
		t.Error("enabled filter=no should contain disabled combo")
	}
}

func TestModelSetDetail_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	sess := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")
	st.CreateChannel(&model.Channel{
		Name: "ch1", Provider: "openai", Protocol: "openai",
		BaseURL: "https://x", Models: []string{"m1"}, Status: model.ChannelEnabled,
	})
	c := newCombo(t, st, tk.ID, "myset")
	c.Models = []string{"m1"}
	if err := st.UpdateComboModel(c); err != nil {
		t.Fatalf("update: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/model-sets/"+itoa(c.ID), nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !contains(rec.Body.String(), "myset") {
		t.Error("body should contain combo name")
	}
	if !contains(rec.Body.String(), "ch1") {
		t.Error("body should contain channel name")
	}
}

func TestModelSetDetail_NoChannel(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	sess := sessionCookieFor(t, st, admin)
	tk := newComboToken(t, st, "tk1")
	c := newCombo(t, st, tk.ID, "orphan")
	c.Models = []string{"nonexistent-model"}
	if err := st.UpdateComboModel(c); err != nil {
		t.Fatalf("update: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/model-sets/"+itoa(c.ID), nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if !contains(rec.Body.String(), "没有启用通道") {
		t.Error("body should warn about missing channel")
	}
}

func TestModelSetDetail_BadID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	sess := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/model-sets/abc", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestModelSetDetail_NotFound(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	sess := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/model-sets/99999", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
