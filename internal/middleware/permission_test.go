package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/rbac"
)

func withUser(role model.UserRole, perms string) context.Context {
	return context.WithValue(context.Background(), UserKey, &model.User{
		Role:        role,
		Permissions: perms,
	})
}

func TestRequirePermission_AdminGranted(t *testing.T) {
	mw := RequirePermission(rbac.PermChannelsWrite)
	hits := 0
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(withUser(model.RoleAdmin, ""))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	if hits != 1 {
		t.Errorf("hits=%d, want 1", hits)
	}
}

func TestRequirePermission_ViewerDenied(t *testing.T) {
	mw := RequirePermission(rbac.PermChannelsWrite)
	hits := 0
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(withUser(model.RoleUser, ""))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 403 {
		t.Fatalf("status=%d, want 403", w.Code)
	}
	if hits != 0 {
		t.Errorf("hits=%d, want 0", hits)
	}
}

func TestRequirePermission_ExplicitGrant(t *testing.T) {
	// viewer with +channels:write override.
	mw := RequirePermission(rbac.PermChannelsWrite)
	hits := 0
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(withUser(model.RoleUser, `["+channels:write"]`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status=%d, want 200 (explicit grant)", w.Code)
	}
	if hits != 1 {
		t.Errorf("hits=%d, want 1", hits)
	}
}

func TestRequirePermission_ExplicitRevoke(t *testing.T) {
	// admin with -channels:delete override.
	mw := RequirePermission(rbac.PermChannelsDelete)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(withUser(model.RoleAdmin, `["-channels:delete"]`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 403 {
		t.Fatalf("status=%d, want 403 (explicit revoke)", w.Code)
	}
}

func TestRequirePermission_NoUser(t *testing.T) {
	mw := RequirePermission(rbac.PermChannelsRead)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Fatalf("status=%d, want 401 (no session)", w.Code)
	}
}

func TestRequirePermission_RootHasAll(t *testing.T) {
	for _, p := range rbac.AllPermissions() {
		mw := RequirePermission(p)
		h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		req := httptest.NewRequest("GET", "/", nil)
		req = req.WithContext(withUser(model.RoleRoot, ""))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("root denied %s (status=%d)", p, w.Code)
		}
	}
}