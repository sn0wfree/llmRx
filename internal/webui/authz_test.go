package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

// TestRoleUserForbiddenFromAdminResources: a logged-in RoleUser
// must not reach resource-management routes (channels list here,
// representative of the whole authenticated group).
func TestRoleUserForbiddenFromAdminResources(t *testing.T) {
	h, _ := newTestWebUI(t)
	_, cookie := newTestUser(t, h, h.store, withRole(model.RoleUser))

	req := httptest.NewRequest(http.MethodGet, "/channels", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("RoleUser /channels: status = %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestRoleUserForbiddenFromWrites: the same gate applies to POSTs.
func TestRoleUserForbiddenFromWrites(t *testing.T) {
	h, _ := newTestWebUI(t)
	_, cookie := newTestUser(t, h, h.store, withRole(model.RoleUser))

	req := httptest.NewRequest(http.MethodPost, "/channels", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("RoleUser POST: status = %d, want 403", rec.Code)
	}
}

// TestRoleAdminAllowed: a RoleAdmin session passes the gate.
func TestRoleAdminAllowed(t *testing.T) {
	h, _ := newTestWebUI(t)
	_, cookie := newTestUser(t, h, h.store, withRole(model.RoleAdmin))

	req := httptest.NewRequest(http.MethodGet, "/channels", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("RoleAdmin blocked: 403 (body: %s)", rec.Body.String())
	}
	if rec.Code < 200 || rec.Code >= 400 {
		t.Fatalf("RoleAdmin /channels: status = %d, want 2xx/3xx", rec.Code)
	}
}
