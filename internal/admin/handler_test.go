package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func do(t *testing.T, h http.Handler, method, path, sess string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var br *strings.Reader
	if body != "" {
		br = strings.NewReader(body)
	} else {
		br = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, br)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if sess != "" {
		req.Header.Set("X-Session-Token", sess)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.NewDecoder(rec.Body).Decode(dst); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
}

func login(t *testing.T, app *testhelper.App) string {
	t.Helper()
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/login", "", `{"username":"admin","password":"admin"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		SessionToken string `json:"session_token"`
	}
	decodeJSON(t, rec, &resp)
	if resp.SessionToken == "" {
		t.Fatal("login: empty session token")
	}
	return resp.SessionToken
}

func TestAdmin_LoginBadCreds(t *testing.T) {
	app := testhelper.New(t)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/login", "", `{"username":"admin","password":"wrong"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAdmin_LoginMissingFields(t *testing.T) {
	app := testhelper.New(t)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/login", "", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAdmin_DashboardRequiresSession(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-aaaaaaaaaaaa")
	app.AddToken("sk-tok", "t")

	rec := do(t, app.Admin.Routes(), http.MethodGet, "/dashboard", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: expected 401, got %d", rec.Code)
	}

	sess := login(t, app)
	rec = do(t, app.Admin.Routes(), http.MethodGet, "/dashboard", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("with session: expected 200, got %d %s", rec.Code, rec.Body.String())
	}
	var dash struct {
		ChannelsTotal  int            `json:"channels_total"`
		TokensTotal    int            `json:"tokens_total"`
		KeysByChannel  map[string]int `json:"keys_by_channel"`
	}
	decodeJSON(t, rec, &dash)
	if dash.ChannelsTotal != 1 || dash.TokensTotal != 1 || dash.KeysByChannel["1"] != 1 {
		t.Fatalf("dashboard: got %+v", dash)
	}
}

func TestAdmin_LogoutInvalidatesSession(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	rec := do(t, app.Admin.Routes(), http.MethodPost, "/logout", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("logout: %d", rec.Code)
	}
	rec = do(t, app.Admin.Routes(), http.MethodGet, "/dashboard", sess, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("reused session: expected 403, got %d", rec.Code)
	}
}

func TestAdmin_ChannelsCRUD(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	// Create
	body := `{"name":"deepseek","provider":"deepseek","base_url":"https://api.deepseek.com/v1","models":["deepseek-chat"],"priority":10,"input_price_per_1m":0.14,"output_price_per_1m":0.42}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	// List
	rec = do(t, app.Admin.Routes(), http.MethodGet, "/channels", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	var list struct {
		Data []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	decodeJSON(t, rec, &list)
	if len(list.Data) != 1 || list.Data[0].Name != "deepseek" {
		t.Fatalf("list: %+v", list)
	}

	// Update (status=2 disabled)
	rec = do(t, app.Admin.Routes(), http.MethodPut, "/channels/1", sess, `{"status":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}

	// Delete
	rec = do(t, app.Admin.Routes(), http.MethodDelete, "/channels/1", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
}

func TestAdmin_ChannelCreateDuplicateName(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	body := `{"name":"dup","provider":"x","base_url":"y","models":["m"]}`

	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("first create: %d", rec.Code)
	}
	rec = do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess, body)
	if rec.Code < 400 {
		t.Fatalf("duplicate: expected 4xx, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_KeysCRUD(t *testing.T) {
	app := testhelper.New(t)
	ch := app.AddChannel("c", "openai", "https://x", []string{"m"})
	sess := login(t, app)

	rec := do(t, app.Admin.Routes(), http.MethodPost,
		"/channels/"+itoa(ch.ID)+"/keys", sess,
		`{"key":"sk-test-key-1234567890"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create key: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, app.Admin.Routes(), http.MethodGet,
		"/channels/"+itoa(ch.ID)+"/keys", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list keys: %d", rec.Code)
	}
	var lst struct {
		Data []struct {
			ID        int64  `json:"id"`
			KeyMasked string `json:"key_masked"`
			Key       string `json:"key,omitempty"`
		} `json:"data"`
	}
	decodeJSON(t, rec, &lst)
	if len(lst.Data) != 1 || lst.Data[0].KeyMasked != "sk-t***7890" {
		t.Fatalf("key list: %+v", lst)
	}
	if lst.Data[0].Key != "" {
		t.Fatalf("key should be masked in list response, got %q", lst.Data[0].Key)
	}

	rec = do(t, app.Admin.Routes(), http.MethodDelete,
		"/channels/"+itoa(ch.ID)+"/keys/"+itoa(lst.Data[0].ID), sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete key: %d", rec.Code)
	}
}

func TestAdmin_TokensIssueAndRevoke(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	rec := do(t, app.Admin.Routes(), http.MethodPost, "/tokens", sess,
		`{"name":"created-via-api","expires_in_days":7,"rpm":60}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create token: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID   int64  `json:"id"`
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	decodeJSON(t, rec, &out)
	if out.Key == "" || out.Name != "created-via-api" {
		t.Fatalf("token response: %+v", out)
	}

	// List (key masked)
	rec = do(t, app.Admin.Routes(), http.MethodGet, "/tokens", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list tokens: %d", rec.Code)
	}
	var lst struct {
		Data []struct {
			ID  int64  `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	decodeJSON(t, rec, &lst)
	if lst.Data[0].Key != "" {
		t.Fatalf("token key should be masked in list, got %q", lst.Data[0].Key)
	}

	// Revoke
	rec = do(t, app.Admin.Routes(), http.MethodDelete, "/tokens/"+itoa(out.ID), sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d", rec.Code)
	}
	// Cache should no longer resolve the key
	if _, ok := app.Cache.Lookup(out.Key); ok {
		t.Fatal("token still resolvable in cache after revoke")
	}
}

func TestAdmin_UsersListAndCreate(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	rec := do(t, app.Admin.Routes(), http.MethodPost, "/users", sess,
		`{"username":"alice","password":"s3cret","role":10}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create user: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, app.Admin.Routes(), http.MethodGet, "/users", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list users: %d", rec.Code)
	}
	var lst struct {
		Data []struct {
			Username     string `json:"username"`
			PasswordHash string `json:"password_hash"`
		} `json:"data"`
	}
	decodeJSON(t, rec, &lst)
	if len(lst.Data) < 2 {
		t.Fatalf("expected ≥2 users, got %d", len(lst.Data))
	}
	for _, u := range lst.Data {
		if u.PasswordHash != "" {
			t.Fatalf("password_hash should be empty in response, got %q", u.PasswordHash)
		}
	}
}

func TestAdmin_DeleteDefaultAdminProtected(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/users/1", sess, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for protected admin, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_LogsList(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-key1234567890")
	app.AddToken("sk-t", "t")
	sess := login(t, app)

	rec := do(t, app.Admin.Routes(), http.MethodGet, "/logs?limit=10", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("logs: %d %s", rec.Code, rec.Body.String())
	}
	var lst struct {
		Data []map[string]any `json:"data"`
	}
	decodeJSON(t, rec, &lst)
	// Empty logs is fine.
	if lst.Data == nil {
		t.Fatal("expected data array, got nil")
	}
}

func TestAdmin_AdminOnlyMiddlewareOnInvalidSession(t *testing.T) {
	app := testhelper.New(t)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/dashboard", "garbage-session", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for invalid session, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "invalid_session") {
		t.Fatalf("expected invalid_session code in body, got %s", body)
	}
	// sanity: assert TokenKey is a real contextKey constant
	_ = middleware.TokenKey
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}