package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestLogsStream_NoBridge(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/logs/stream", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d want 503", rec.Code)
	}
}

func TestLogsStream_WithBridge(t *testing.T) {
	h, st := newTestWebUI(t)
	bridge := NewWebAPIBridge(st)
	h.adminH = bridge
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	ctx, cancel := contextWithTimeout()
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/logs/stream", nil)
	req = req.WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type=%q want text/event-stream", ct)
	}
}

func TestNewWebAPIBridge(t *testing.T) {
	_, st := newTestWebUI(t)
	b := NewWebAPIBridge(st)
	if b == nil {
		t.Fatal("bridge should not be nil")
	}
	if b.store == nil {
		t.Error("store should be set")
	}
}

func TestWebAPIBridge_SetReloader(t *testing.T) {
	_, st := newTestWebUI(t)
	b := NewWebAPIBridge(st)
	called := false
	b.SetReloader(func() error {
		called = true
		return nil
	})
	if err := b.TriggerReload(); err != nil {
		t.Fatalf("TriggerReload: %v", err)
	}
	if !called {
		t.Error("reloader should be called")
	}
}

func TestWebAPIBridge_Store(t *testing.T) {
	_, st := newTestWebUI(t)
	b := NewWebAPIBridge(st)
	if b.Store() == nil {
		t.Error("Store() should return non-nil")
	}
}

func TestRequireRole_SufficientRole(t *testing.T) {
	h, st := newTestWebUI(t)
	hash, _ := hashForTest("adminpw")
	admin2 := &model.User{Username: "admin2", PasswordHash: hash, Role: model.RoleAdmin, Status: 1}
	st.CreateUser(admin2)
	tok := sessionCookieFor(t, st, admin2)

	// /users is behind RequireRole(RoleRoot). RoleAdmin should get 403.
	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d want 403 for RoleAdmin on /users", rec.Code)
	}
}
