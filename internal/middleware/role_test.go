package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestRequireRole_NoUser(t *testing.T) {
	mw := RequireRole(model.RoleAdmin)
	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
	h.ServeHTTP(rec, req)
	if called {
		t.Fatal("handler should not be called")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestRequireRole_InsufficientRole(t *testing.T) {
	mw := RequireRole(model.RoleRoot)
	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserKey, &model.User{ID: 1, Role: model.RoleUser}))
	h.ServeHTTP(rec, req)
	if called {
		t.Fatal("handler should not be called")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestRequireRole_SufficientRole(t *testing.T) {
	mw := RequireRole(model.RoleAdmin)
	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserKey, &model.User{ID: 1, Role: model.RoleRoot}))
	h.ServeHTTP(rec, req)
	if !called {
		t.Fatal("handler should be called")
	}
}

func TestRequireRole_ExactRole(t *testing.T) {
	mw := RequireRole(model.RoleAdmin)
	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
	req = req.WithContext(context.WithValue(req.Context(), UserKey, &model.User{ID: 1, Role: model.RoleAdmin}))
	h.ServeHTTP(rec, req)
	if !called {
		t.Fatal("handler should be called with exact role")
	}
}

func TestReadCookie_Middleware(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "sess", Value: "tok"})
	if got := readCookie(req, "sess"); got != "tok" {
		t.Fatalf("readCookie: got %q", got)
	}
	if got := readCookie(req, "missing"); got != "" {
		t.Fatalf("readCookie missing: got %q", got)
	}
}
