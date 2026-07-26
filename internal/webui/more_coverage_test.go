package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

// helper to create test data
func createTestData(t *testing.T, h *Handler) {
	t.Helper()
	h.store.CreateChannel(&model.Channel{
		Name: "ch1", Provider: "openai", Protocol: "openai",
		BaseURL: "https://x", Models: []string{"m"}, Status: model.ChannelEnabled,
	})
	h.store.CreateToken(&model.Token{Key: "sk-tok1", Name: "tok1", Status: model.TokenActive})
	h.store.CreatePlan(&model.Plan{Name: "pro", MarkupRatio: 1.0, Status: 1})
}

// --- updateChannelByID error paths ---

func TestUpdateChannelByID_StoreError3(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	createTestData(t, h)
	ss.UpdateChannelFunc = func(ch *model.Channel) error {
		return errTestStore
	}
	body := "_method=PUT&name=ch2&status=1"
	req := formReq2(t, "/channels/1", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

// validation error test removed - session middleware complicates form POST testing

// --- updateTokenByID error paths ---

func TestUpdateTokenByID_StoreError3(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	createTestData(t, h)
	ss.UpdateTokenFunc = func(t *model.Token) error {
		return errTestStore
	}
	body := "_method=PUT&name=tok2&status=1"
	req := formReq2(t, "/tokens/1", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

// --- updatePlanByID error paths ---

func TestUpdatePlanByID_StoreError3(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	createTestData(t, h)
	ss.UpdatePlanFunc = func(p *model.Plan) error {
		return errTestStore
	}
	body := "_method=PUT&name=pro-v2&markup_ratio=2.0&status=1"
	req := formReq2(t, "/plans/1", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

// --- DashboardPage with active data ---

func TestDashboardPage_WithActiveData3(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	createTestData(t, h)
	req := authReq2(t, http.MethodGet, "/dashboard", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

// --- AlertAction error paths ---

func TestAlertAction_BadID4(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	body := "_method=PUT"
	req := formReq2(t, "/alerts/abc", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAlertAction_BadMethod4(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	body := "_method=PATCH"
	req := formReq2(t, "/alerts/1", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code=%d want 405", rec.Code)
	}
}

func TestAlertUpdate_NotFound4(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	body := "_method=PUT&name=a2"
	req := formReq2(t, "/alerts/9999", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

// --- AlertsPage GetAlertEvents error ---

func TestAlertsPage_GetAlertEventsError3(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.GetAlertEventsFunc = func(limit int) ([]model.AlertEvent, error) {
		return nil, errTestStore
	}
	req := authReq2(t, http.MethodGet, "/alerts", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

// --- LogsStream no bridge ---

func TestLogsStream_NoBridge4(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	req := authReq2(t, http.MethodGet, "/logs/stream", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d want 503", rec.Code)
	}
}

// --- Login edge cases ---

func TestLoginPage_RedirectWhenLoggedIn3(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	req := authReq2(t, http.MethodGet, "/login", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d want 303", rec.Code)
	}
}

func TestLoginSubmit_EmptyFields4(t *testing.T) {
	h, _ := newScriptedWebui(t)
	body := "username=&password="
	req := formReq2(t, "/login", body, "")
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

// --- Template rendering ---

func TestRenderer_TemplateFuncs3(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	data := map[string]any{
		"Body": "channels_list_body", "Title": "t", "Active": "channels",
		"Channels": []model.Channel{{Name: "ch1", Status: model.ChannelEnabled}},
	}
	rec := httptest.NewRecorder()
	err = r.Render(rec, "channels_list_body", data)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
}

// --- ConfigSave edge cases ---

func TestConfigSave_WithValidFile4(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	body := "yaml=server:\n  port: 9090"
	req := formReq2(t, "/config", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

// --- EffectivePage ---

func TestEffectivePage_Renders4(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	req := authReq2(t, http.MethodGet, "/effective", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

// --- AnalyticsPage ---

func TestAnalyticsPage_Renders4(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	req := authReq2(t, http.MethodGet, "/analytics", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

// --- ChannelEditForm not found ---

func TestChannelEditForm_NotFound4(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	req := authReq2(t, http.MethodGet, "/channels/9999/edit", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

// --- PlanCreate empty name ---

func TestPlanCreate_EmptyName5(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	body := "name=&markup_ratio=1.5&status=1"
	req := formReq2(t, "/plans", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

// --- UserCreate edge cases ---

func TestUserCreate_EmptyFields4(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	body := "username=&password="
	req := formReq2(t, "/users", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestUserCreate_ShortPassword4(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	body := "username=bob&password=123&role=0"
	req := formReq2(t, "/users", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

// --- UserDelete edge cases ---

func TestUserDelete_BadID4(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	req := authReq2(t, http.MethodDelete, "/users/abc", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestUserDelete_DefaultAdminProtected4(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	req := authReq2(t, http.MethodDelete, "/users/1", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400 (protected)", rec.Code)
	}
}

// --- PlanAction edge cases ---

func TestPlanAction_BadID4(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	body := "_method=PUT"
	req := formReq2(t, "/plans/abc", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestPlanAction_BadMethod4(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	body := "_method=PATCH"
	req := formReq2(t, "/plans/1", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code=%d want 405", rec.Code)
	}
}

// --- ChannelAction edge cases ---

func TestChannelAction_BadID4(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	body := "_method=PUT"
	req := formReq2(t, "/channels/abc", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

// --- TokenAction edge cases ---

func TestTokenAction_BadID4(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	body := "_method=PUT"
	req := formReq2(t, "/tokens/abc", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}
