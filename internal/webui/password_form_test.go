package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

// TestUserPasswordForm_RendersTargetUser: the change-password page
// must render the password form for the URL's target user, not the
// new-user form (which used to be the case — Render dispatches on
// .Body, so an inconsistent Body field rendered the wrong template).
func TestUserPasswordForm_RendersTargetUser(t *testing.T) {
	h, _ := newTestWebUI(t)
	_, cookie := newTestUser(t, h, h.store, withRole(model.RoleRoot))
	alice := &model.User{Username: "alice", Role: model.RoleUser, Status: 1, PasswordHash: "$argon2id$dummy"}
	if err := h.store.CreateUser(alice); err != nil {
		t.Fatalf("create alice: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/users/"+itoa(alice.ID)+"/password", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "修改密码：alice") {
		t.Errorf("page must target alice:\n%s", body)
	}
	wantAction := "/admin/users/" + itoa(alice.ID) + "/password"
	if !strings.Contains(body, `action="`+wantAction+`"`) {
		t.Errorf("form action must be %q:\n%s", wantAction, body)
	}
	// New-user form fields must be absent.
	if strings.Contains(body, `name="username"`) {
		t.Errorf("new-user form rendered instead of password form:\n%s", body)
	}
}

// TestUserPasswordSubmit_ChangesTargetUser: RoleRoot changing
// alice's password updates alice, not the caller.
func TestUserPasswordSubmit_ChangesTargetUser(t *testing.T) {
	h, _ := newTestWebUI(t)
	root, rootCookie := newTestUser(t, h, h.store, withRole(model.RoleRoot))
	alice := &model.User{Username: "alice", Role: model.RoleUser, Status: 1, PasswordHash: "$argon2id$dummy"}
	if err := h.store.CreateUser(alice); err != nil {
		t.Fatalf("create alice: %v", err)
	}

	rec := formPostWithCookie(t, h.Routes(), "/users/"+itoa(alice.ID)+"/password", rootCookie,
		map[string]string{"password": "newsecret"})
	if rec.Code < 300 || rec.Code >= 400 {
		t.Fatalf("status = %d, want 3xx", rec.Code)
	}

	updated, err := h.store.GetUser(alice.ID)
	if err != nil {
		t.Fatalf("GetUser(alice): %v", err)
	}
	if updated.PasswordHash == alice.PasswordHash {
		t.Error("alice's password hash did not change")
	}
	rootAfter, err := h.store.GetUser(root.ID)
	if err != nil {
		t.Fatalf("GetUser(root): %v", err)
	}
	if rootAfter.PasswordHash != root.PasswordHash {
		t.Error("root's password changed instead of alice's")
	}
}
