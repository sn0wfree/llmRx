package webui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// formPost builds and executes a POST request with form-encoded
// fields. Saves ~6 lines of header boilerplate per test.
func formPost(t *testing.T, h http.Handler, path string, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	values := url.Values{}
	for k, v := range fields {
		values.Set(k, v)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// formPostWithCookie adds a session cookie before dispatching.
func formPostWithCookie(t *testing.T, h http.Handler, path, cookie string, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	values := url.Values{}
	for k, v := range fields {
		values.Set(k, v)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// assertContains fails the test if substr is not in s.
func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !contains(s, substr) {
		t.Errorf("expected body to contain %q\nbody: %s", substr, s)
	}
}

// assertNotContains fails the test if substr IS in s. Used to
// guard against accidental rendering (e.g. sensitive data leaked
// into a partial).
func assertNotContains(t *testing.T, s, substr string) {
	t.Helper()
	if contains(s, substr) {
		t.Errorf("expected body to NOT contain %q\nbody: %s", substr, s)
	}
}

// assertStatus fails if the recorder status doesn't match.
func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d\nbody: %s", rec.Code, want, rec.Body.String())
	}
}

// assertRedirect checks that rec is a 3xx with a Location header
// pointing to prefix. Used by every successful form submit.
func assertRedirect(t *testing.T, rec *httptest.ResponseRecorder, prefix string) {
	t.Helper()
	if rec.Code < 300 || rec.Code >= 400 {
		t.Fatalf("status = %d, want 3xx\nbody: %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, prefix) {
		t.Fatalf("Location = %q, want prefix %q", loc, prefix)
	}
}

// newTestUser creates a user with the given role. Returns the
// user and a session cookie value for authenticated requests.
// Optional: callers can pass nil to use a default admin (role 100).
func newTestUser(t *testing.T, h *Handler, st WebuiStore, opts ...userOpt) (*model.User, string) {
	t.Helper()
	u := &model.User{
		Username:     "tester",
		Role:         model.RoleAdmin,
		Status:       1,
		PasswordHash: "$argon2id$dummy",
	}
	for _, opt := range opts {
		opt(u)
	}
	if err := st.CreateUser(u); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	tok := newSessionToken()
	u.SessionToken = tok
	exp := nowAdd(DefaultSessionTTL)
	u.SessionExp = &exp
	if err := st.UpdateUser(u); err != nil {
		t.Fatalf("update user session: %v", err)
	}
	return u, tok
}

type userOpt func(*model.User)

func withUsername(name string) userOpt {
	return func(u *model.User) { u.Username = name }
}

func withRole(r model.UserRole) userOpt {
	return func(u *model.User) { u.Role = r }
}

// modelChannelFixture builds a minimal channel for tests that
// just need *some* channel in the database. Status defaults to
// enabled; callers can override after CreateChannel.
func modelChannelFixture() *model.Channel {
	return &model.Channel{
		Name:      "test-channel",
		Provider:  "openai",
		Protocol:  "openai",
		BaseURL:   "https://api.example.com/v1",
		Models:    []string{"gpt-4"},
		Intents:   []string{"chat"},
		Priority:  5,
		Status:    model.ChannelEnabled,
		CreatedAt: timeNow(),
		UpdatedAt: timeNow(),
	}
}

func timeNow() time.Time { return time.Now() }
