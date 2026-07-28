package webui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/auth"
	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

// newTestWebUI builds a webui.Handler backed by a real in-process
// store. Used by the auth-gate tests below.
func newTestWebUI(t *testing.T) (*Handler, store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	logDir := filepath.Join(dir, "logs")
	if err := logstore.EnsureDir(logDir); err != nil {
		t.Fatalf("logstore.EnsureDir: %v", err)
	}
	logStore, err := logstore.New(logDir, nil)
	if err != nil {
		t.Fatalf("logstore.New: %v", err)
	}
	t.Cleanup(func() { _ = logStore.Close() })

	// Seed default admin (RoleRoot).
	hash, _ := auth.Hash("admin")
	if err := st.CreateUser(&model.User{
		Username: "admin", PasswordHash: hash, Role: model.RoleRoot, Status: 1,
	}); err != nil {
		t.Fatalf("seed admin: %v", err)
	}

	h, err := New(st, logStore, nil, "")
	if err != nil {
		t.Fatalf("webui.New: %v", err)
	}
	return h, st
}

// sessionCookieFor mints a fresh session token for the given user
// and writes it through the store so SessionMiddleware accepts it.
func sessionCookieFor(t *testing.T, st store.Store, u *model.User) string {
	t.Helper()
	tok := newSessionToken()
	u.SessionToken = tok
	exp := nowAdd(DefaultSessionTTL)
	u.SessionExp = &exp
	if err := st.UpdateUser(u); err != nil {
		t.Fatalf("update user session: %v", err)
	}
	return tok
}

func TestUserPasswordForm_AllowsOwnPage(t *testing.T) {
	h, st := newTestWebUI(t)
	// Create a non-root user.
	hash, _ := auth.Hash("alice-pw-123")
	alice := &model.User{Username: "alice", PasswordHash: hash, Role: model.RoleUser, Status: 1}
	if err := st.CreateUser(alice); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	tok := sessionCookieFor(t, st, alice)

	// Routes() returns the inner chi router; the production
	// mount prefix (/admin) is applied by server.go, so we hit
	// the inner path directly.
	req := httptest.NewRequest(http.MethodGet, "/users/"+itoa(alice.ID)+"/password", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET own password form: code=%d body=%q", w.Code, w.Body.String())
	}
}

func TestUserPasswordForm_ForbiddenForOtherUser(t *testing.T) {
	h, st := newTestWebUI(t)
	// Two non-root users; alice tries to view bob's password form.
	aliceHash, _ := auth.Hash("alice-pw-123")
	alice := &model.User{Username: "alice", PasswordHash: aliceHash, Role: model.RoleUser, Status: 1}
	bobHash, _ := auth.Hash("bob-pw-123")
	bob := &model.User{Username: "bob", PasswordHash: bobHash, Role: model.RoleUser, Status: 1}
	if err := st.CreateUser(alice); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if err := st.CreateUser(bob); err != nil {
		t.Fatalf("create bob: %v", err)
	}
	tok := sessionCookieFor(t, st, alice)

	req := httptest.NewRequest(http.MethodGet, "/users/"+itoa(bob.ID)+"/password", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("GET other-user password form: code=%d (want 403) body=%q", w.Code, w.Body.String())
	}
}

func TestUserPasswordForm_RootCanViewAnyUser(t *testing.T) {
	h, st := newTestWebUI(t)
	// Root (admin) can view any user's password form.
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	aliceHash, _ := auth.Hash("alice-pw-123")
	alice := &model.User{Username: "alice", PasswordHash: aliceHash, Role: model.RoleUser, Status: 1}
	if err := st.CreateUser(alice); err != nil {
		t.Fatalf("create alice: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/users/"+itoa(alice.ID)+"/password", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("root GET any-user password form: code=%d (want 200) body=%q", w.Code, w.Body.String())
	}
}

func TestUserPasswordForm_AnonymousRedirectsToLogin(t *testing.T) {
	h, _ := newTestWebUI(t)

	req := httptest.NewRequest(http.MethodGet, "/users/1/password", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("anonymous GET password form: code=%d (want 303) body=%q", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "/admin/login") {
		t.Fatalf("redirect Location = %q, want /admin/login", loc)
	}
}

// itoa is a tiny helper to keep the test bodies compact.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// _ keeps chi imported even if no test references the router
// directly — Routes() returns the chi router type and we want to
// be sure the import is exercised.
var _ = chi.NewRouter