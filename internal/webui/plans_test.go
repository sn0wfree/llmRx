package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestPlansPage_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	st.CreatePlan(&model.Plan{Name: "pro", MarkupRatio: 1.5, Status: 1})

	req := httptest.NewRequest(http.MethodGet, "/plans", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPlanNewForm_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/plans/new", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestPlanCreate_Success(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	body := "name=pro&budget_usd=100&markup_ratio=1.5&status=1"
	req := httptest.NewRequest(http.MethodPost, "/plans", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	plans, _ := st.GetPlans()
	if len(plans) != 1 || plans[0].Name != "pro" {
		t.Errorf("expected 1 plan pro, got %+v", plans)
	}
}

func TestPlanCreate_EmptyName(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	body := "name=&budget_usd=0&markup_ratio=1&status=1"
	req := httptest.NewRequest(http.MethodPost, "/plans", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	plans, _ := st.GetPlans()
	if len(plans) != 0 {
		t.Errorf("no plan should be created")
	}
}

func TestPlanEditForm_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	p := &model.Plan{Name: "pro", MarkupRatio: 1.5, Status: 1}
	st.CreatePlan(p)

	req := httptest.NewRequest(http.MethodGet, "/plans/"+itoa(p.ID)+"/edit", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPlanEditForm_BadID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/plans/abc/edit", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestPlanAction_Update(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	p := &model.Plan{Name: "pro", MarkupRatio: 1.0, Status: 1}
	st.CreatePlan(p)

	body := "_method=PUT&name=pro-v2&budget_usd=200&markup_ratio=2.0&status=1"
	req := httptest.NewRequest(http.MethodPost, "/plans/"+itoa(p.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	updated, _ := st.GetPlan(p.ID)
	if updated.Name != "pro-v2" {
		t.Errorf("name=%q want pro-v2", updated.Name)
	}
	if updated.MarkupRatio != 2.0 {
		t.Errorf("markup=%v want 2.0", updated.MarkupRatio)
	}
}

func TestPlanAction_Delete(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	p := &model.Plan{Name: "pro", MarkupRatio: 1.0, Status: 1}
	st.CreatePlan(p)

	body := "_method=DELETE"
	req := httptest.NewRequest(http.MethodPost, "/plans/"+itoa(p.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	plans, _ := st.GetPlans()
	if len(plans) != 0 {
		t.Errorf("plan should be deleted")
	}
}

func TestPlanAction_Delete_UnlinksTokens(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	p := &model.Plan{Name: "pro", MarkupRatio: 1.0, Status: 1}
	st.CreatePlan(p)
	tk := &model.Token{Key: "sk-t1", Name: "tok1", PlanID: p.ID, Status: model.TokenActive}
	st.CreateToken(tk)

	body := "_method=DELETE"
	req := httptest.NewRequest(http.MethodPost, "/plans/"+itoa(p.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	updated, _ := st.GetTokenByID(tk.ID)
	if updated.PlanID != 0 {
		t.Errorf("token plan_id should be unlinked, got %d", updated.PlanID)
	}
}
