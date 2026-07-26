package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginSubmit_Success(t *testing.T) {
	h, st := newTestWebUI(t)
	body := "username=admin&password=admin"
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	updated, _ := st.GetUserByUsername("admin")
	if updated.SessionToken == "" {
		t.Errorf("session token should be set")
	}
}

func TestLoginSubmit_BadPassword(t *testing.T) {
	h, _ := newTestWebUI(t)
	body := "username=admin&password=wrong"
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "\u7528\u6237\u540d\u6216\u5bc6\u7801\u9519\u8bef") {
		t.Errorf("body should contain error: %s", rec.Body.String())
	}
}

func TestLoginSubmit_EmptyFields(t *testing.T) {
	h, _ := newTestWebUI(t)
	body := "username=&password="
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "\u8bf7\u8f93\u5165") {
		t.Errorf("body should contain error: %s", rec.Body.String())
	}
}

func TestLoginPage_AlreadyLoggedIn(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/dashboard" {
		t.Errorf("location=%q want /admin/dashboard", loc)
	}
}

func TestLogoutSubmit_ClearsSession(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d want 303", rec.Code)
	}
	updated, _ := st.GetUserByUsername("admin")
	if updated.SessionToken != "" {
		t.Errorf("session token should be cleared")
	}
}

func TestDashboardPage_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRootRedirect(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d want 303", rec.Code)
	}
}
