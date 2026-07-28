package webui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/auth"
	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

var errTestStore = errors.New("test store error")

type errorLogDriver struct{ err error }

func (d *errorLogDriver) Open(dir string) error                                          { return nil }
func (d *errorLogDriver) Insert(entry *model.Log) error                                 { return d.err }
func (d *errorLogDriver) QueryAcross(f logstore.QueryFilter, days []string) ([]model.Log, int64, error) {
	return nil, 0, d.err
}
func (d *errorLogDriver) LogStats(days []string) (logstore.LogStatsResult, error) {
	return logstore.LogStatsResult{}, d.err
}
func (d *errorLogDriver) TimeSeries(f logstore.QueryFilter, bucketSec int64, days []string) ([]logstore.SeriesBucket, error) {
	return nil, d.err
}
func (d *errorLogDriver) TopByField(f logstore.QueryFilter, field string, limit int, days []string) ([]logstore.NamedMetric, error) {
	return nil, d.err
}
func (d *errorLogDriver) ListFiles() ([]string, error)   { return nil, d.err }
func (d *errorLogDriver) DeleteFiles(days []string) error { return d.err }
func (d *errorLogDriver) Close() error                  { return nil }

func newErrorLogStore(t *testing.T) *logstore.Manager {
	t.Helper()
	ls, err := logstore.New(t.TempDir(), &errorLogDriver{err: errTestStore})
	if err != nil {
		t.Fatalf("newErrorLogStore: %v", err)
	}
	return ls
}

// newScriptedWebui creates a webui handler backed by a ScriptedStore
// without importing testhelper (which would create an import cycle).
func newScriptedWebui(t *testing.T) (*Handler, *ScriptedStore) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	logDir := filepath.Join(dir, "logs")
	logstore.EnsureDir(logDir)
	ls, _ := logstore.New(logDir, nil)
	t.Cleanup(func() { _ = ls.Close() })

	hash, _ := auth.Hash("admin")
	st.CreateUser(&model.User{
		Username: "admin", PasswordHash: hash, Role: model.RoleRoot, Status: 1,
	})

	ss := NewScriptedStore(st)
	h, err := New(ss, ls, nil, "")
	if err != nil {
		t.Fatalf("webui.New: %v", err)
	}
	return h, ss
}

// testSession returns a valid session cookie for the admin user.
func testSession(t *testing.T, h *Handler) string {
	t.Helper()
	u, _ := h.store.GetUserByUsername("admin")
	tok := newSessionToken()
	u.SessionToken = tok
	exp := nowAdd(DefaultSessionTTL)
	u.SessionExp = &exp
	h.store.UpdateUser(u)
	return tok
}

func authReq2(t *testing.T, method, path, sess string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if sess != "" {
		req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	}
	return req
}

func formReq2(t *testing.T, path, body, sess string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if sess != "" {
		req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: sess})
	}
	return req
}

// --- Channels error paths ---

func TestChannelsPage_StoreError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.GetChannelsFunc = func() ([]model.Channel, error) {
		return nil, errTestStore
	}
	req := authReq2(t, http.MethodGet, "/channels", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestChannelEditForm_GetChannelError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.GetChannelFunc = func(id int64) (*model.Channel, error) {
		return nil, errTestStore
	}
	req := authReq2(t, http.MethodGet, "/channels/1/edit", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestChannelCreate_StoreError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.CreateChannelFunc = func(ch *model.Channel) error {
		return errTestStore
	}
	body := "name=ch1&provider=openai&base_url=https://x&models=gpt-4"
	req := formReq2(t, "/channels", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestChannelDelete_StoreError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.DeleteChannelFunc = func(id int64) error {
		return errTestStore
	}
	req := authReq2(t, http.MethodDelete, "/channels/1", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestChannelKeysPage_GetChannelError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.GetChannelFunc = func(id int64) (*model.Channel, error) {
		return nil, errTestStore
	}
	req := authReq2(t, http.MethodGet, "/channels/1/keys", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestChannelKeyCreate_StoreError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.CreateKeyFunc = func(k *model.Key) error {
		return errTestStore
	}
	body := "key=sk-test-key-12345"
	req := formReq2(t, "/channels/1/keys", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestChannelKeyDelete_StoreError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.DeleteKeyFunc = func(id int64) error {
		return errTestStore
	}
	req := authReq2(t, http.MethodDelete, "/channels/1/keys/1", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

// --- Tokens error paths ---

func TestTokensPage_StoreError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.GetTokensFunc = func() ([]model.Token, error) {
		return nil, errTestStore
	}
	req := authReq2(t, http.MethodGet, "/tokens", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestTokenEditForm_GetTokenError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.GetTokenByIDFunc = func(id int64) (*model.Token, error) {
		return nil, errTestStore
	}
	req := authReq2(t, http.MethodGet, "/tokens/1/edit", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestTokenCreate_StoreError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.CreateTokenFunc = func(t *model.Token) error {
		return errTestStore
	}
	body := "name=tok1&status=1"
	req := formReq2(t, "/tokens", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestTokenDelete_StoreError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.DeleteTokenFunc = func(id int64) error {
		return errTestStore
	}
	req := authReq2(t, http.MethodDelete, "/tokens/1", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

// --- Plans error paths ---

func TestPlansPage_StoreError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.GetPlansFunc = func() ([]model.Plan, error) {
		return nil, errTestStore
	}
	req := authReq2(t, http.MethodGet, "/plans", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestPlanCreate_StoreError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.CreatePlanFunc = func(p *model.Plan) error {
		return errTestStore
	}
	body := "name=pro&markup_ratio=1.5&status=1"
	req := formReq2(t, "/plans", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestPlanDelete_StoreError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.DeletePlanFunc = func(id int64) error {
		return errTestStore
	}
	body := "_method=DELETE"
	req := formReq2(t, "/plans/1", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

// --- Users error paths ---

func TestUsersPage_StoreError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.GetUsersFunc = func() ([]model.User, error) {
		return nil, errTestStore
	}
	req := authReq2(t, http.MethodGet, "/users", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestUserCreate_StoreError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.CreateUserFunc = func(u *model.User) error {
		return errTestStore
	}
	body := "username=bob&password=bobpw123&role=0"
	req := formReq2(t, "/users", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestUserPasswordForm_GetUserError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.GetUserFunc = func(id int64) (*model.User, error) {
		return nil, errTestStore
	}
	req := authReq2(t, http.MethodGet, "/users/1/password", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestUserPasswordSubmit_StoreError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.UpdateUserFunc = func(u *model.User) error {
		return errTestStore
	}
	body := "password=newpassword123"
	req := formReq2(t, "/users/1/password", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

// --- Phase2 error paths ---

func TestLogsPage_StoreError(t *testing.T) {
	h, _ := newScriptedWebui(t)
	tok := testSession(t, h)
	h.SetLogStore(newErrorLogStore(t))
	req := authReq2(t, http.MethodGet, "/logs", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAlertsPage_StoreError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.GetAlertsFunc = func() ([]model.Alert, error) {
		return nil, errTestStore
	}
	req := authReq2(t, http.MethodGet, "/alerts", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAlertEditForm_GetAlertError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.GetAlertFunc = func(id int64) (*model.Alert, error) {
		return nil, errTestStore
	}
	req := authReq2(t, http.MethodGet, "/alerts/1/edit", tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestAlertDelete_StoreError(t *testing.T) {
	h, ss := newScriptedWebui(t)
	tok := testSession(t, h)
	ss.DeleteAlertFunc = func(id int64) error {
		return errTestStore
	}
	body := "_method=DELETE"
	req := formReq2(t, "/alerts/1", body, tok)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

// --- Login error paths ---

func TestLoginSubmit_BadPassword2(t *testing.T) {
	h, ss := newScriptedWebui(t)
	ss.GetUserByUsernameFunc = func(username string) (*model.User, error) {
		if username == "admin" {
			hash, _ := auth.Hash("admin")
			return &model.User{ID: 1, Username: "admin", PasswordHash: hash, Role: model.RoleRoot, Status: 1}, nil
		}
		return nil, errTestStore
	}

	body := "username=admin&password=wrong"
	req := formReq2(t, "/login", body, "")
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestLoginSubmit_NonexistentUser(t *testing.T) {
	h, ss := newScriptedWebui(t)
	ss.GetUserByUsernameFunc = func(username string) (*model.User, error) {
		return nil, errTestStore
	}

	body := "username=nobody&password=pass"
	req := formReq2(t, "/login", body, "")
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}
