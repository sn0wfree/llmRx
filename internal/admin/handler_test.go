package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
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
		SessionToken      string `json:"session_token"`
		SessionExpiresAt  string `json:"session_expires_at"`
	}
	decodeJSON(t, rec, &resp)
	if resp.SessionToken == "" {
		t.Fatal("login: empty session token")
	}
	if resp.SessionExpiresAt == "" {
		t.Fatal("login: empty session_expires_at")
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

func TestAdmin_SessionExpiry(t *testing.T) {
	app := testhelper.New(t)
	app.Admin.SetSessionTTL(200 * time.Millisecond)
	sess := login(t, app)

	// Still valid
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/dashboard", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("fresh session: %d %s", rec.Code, rec.Body.String())
	}

	// Wait past TTL
	time.Sleep(300 * time.Millisecond)
	rec = do(t, app.Admin.Routes(), http.MethodGet, "/dashboard", sess, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expired session: expected 403, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_LoginPersistsSessionExp(t *testing.T) {
	app := testhelper.New(t)
	app.Admin.SetSessionTTL(2 * time.Hour)
	sess := login(t, app)

	u, err := app.Store.GetUserBySession(sess)
	if err != nil || u == nil {
		t.Fatalf("GetUserBySession: %v", err)
	}
	if u.SessionExp == nil {
		t.Fatal("SessionExp is nil after login")
	}
	if time.Until(*u.SessionExp) < time.Hour {
		t.Fatalf("SessionExp should be ~2h out, got %s", time.Until(*u.SessionExp))
	}
}

func TestAdmin_LogoutClearsSessionExp(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	rec := do(t, app.Admin.Routes(), http.MethodPost, "/logout", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("logout: %d", rec.Code)
	}
	u, _ := app.Store.GetUserBySession(sess)
	if u != nil {
		t.Fatalf("session still resolves after logout: %+v", u)
	}
	u2, _ := app.Store.GetUser(1)
	if u2.SessionExp != nil {
		t.Fatalf("SessionExp should be cleared on logout, got %v", u2.SessionExp)
	}
}

func TestAdmin_CleanupExpiredSessions(t *testing.T) {
	app := testhelper.New(t)
	// First login: live session
	login(t, app)

	// Manually backdate the session
	if u, _ := app.Store.GetUser(1); u != nil {
		u.SessionExp = ptrTime(time.Now().Add(-time.Hour))
		_ = app.Store.UpdateUser(u)
	}

	n, err := app.Store.CleanupExpiredSessions()
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 cleared, got %d", n)
	}
	// Future-dated session should not be cleared
	if u, _ := app.Store.GetUser(1); u != nil {
		u.SessionExp = ptrTime(time.Now().Add(time.Hour))
		_ = app.Store.UpdateUser(u)
	}
	n2, _ := app.Store.CleanupExpiredSessions()
	if n2 != 0 {
		t.Fatalf("expected 0 cleared (future exp), got %d", n2)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestAdmin_LogsFilterByToken(t *testing.T) {
	app := testhelper.New(t)
	ch := app.AddChannel("c", "openai", "https://x", []string{"a"}, "sk-key")
	tok1 := app.AddToken("sk-t1", "first")
	tok2 := app.AddToken("sk-t2", "second")

	now := time.Now()
	for i, tok := range []*model.Token{tok1, tok2} {
		l := &model.Log{
			TokenID: tok.ID, ChannelID: ch.ID, KeyID: 1, Model: "a",
			PromptTokens: 10, CompletionTokens: 5, RealCostUSD: 0.001,
			StatusCode: 200, CreatedAt: now.Add(time.Duration(i) * time.Second),
		}
		if err := app.Store.CreateLog(l); err != nil {
			t.Fatalf("seed log: %v", err)
		}
	}
	sess := login(t, app)

	rec := do(t, app.Admin.Routes(), http.MethodGet, "/logs?token_id="+itoa(tok1.ID), sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data  []model.Log `json:"data"`
		Total int64       `json:"total"`
	}
	decodeJSON(t, rec, &resp)
	if resp.Total != 1 || len(resp.Data) != 1 || resp.Data[0].TokenID != tok1.ID {
		t.Fatalf("filtered: total=%d rows=%+v", resp.Total, resp.Data)
	}
}

func TestAdmin_LogsFilterByChannelAndModel(t *testing.T) {
	app := testhelper.New(t)
	ch := app.AddChannel("c", "openai", "https://x", []string{"a", "b"}, "sk-key")
	tok := app.AddToken("sk-t", "t")

	seeds := []struct {
		model  string
		status int
	}{
		{"a", 200}, {"a", 500}, {"b", 200},
	}
	for _, s := range seeds {
		l := &model.Log{
			TokenID: tok.ID, ChannelID: ch.ID, KeyID: 1, Model: s.model,
			StatusCode: s.status,
		}
		if err := app.Store.CreateLog(l); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	sess := login(t, app)

	rec := do(t, app.Admin.Routes(), http.MethodGet, "/logs?model=a", sess, "")
	var resp struct {
		Data  []model.Log `json:"data"`
		Total int64       `json:"total"`
	}
	decodeJSON(t, rec, &resp)
	if resp.Total != 2 {
		t.Fatalf("model=a: total=%d", resp.Total)
	}

	rec = do(t, app.Admin.Routes(), http.MethodGet, "/logs?model=a&status_code=500", sess, "")
	decodeJSON(t, rec, &resp)
	if resp.Total != 1 || resp.Data[0].StatusCode != 500 {
		t.Fatalf("model=a status=500: %+v", resp.Data)
	}

	rec = do(t, app.Admin.Routes(), http.MethodGet, "/logs?channel_id="+itoa(ch.ID), sess, "")
	decodeJSON(t, rec, &resp)
	if resp.Total != 3 {
		t.Fatalf("channel: total=%d", resp.Total)
	}
}

func TestAdmin_LogsFilterByDateRange(t *testing.T) {
	app := testhelper.New(t)
	ch := app.AddChannel("c", "openai", "https://x", []string{"a"}, "sk-key")
	tok := app.AddToken("sk-t", "t")

	base := time.Now().UTC().Truncate(time.Hour)
	for i, h := range []time.Duration{-3, -1, 2, 5} {
		l := &model.Log{
			TokenID: tok.ID, ChannelID: ch.ID, KeyID: 1, Model: "a",
			StatusCode: 200, CreatedAt: base.Add(h * time.Hour),
		}
		_ = i
		_ = app.Store.CreateLog(l)
	}
	sess := login(t, app)

	from := base.Add(-2 * time.Hour).Format(time.RFC3339)
	to := base.Add(3 * time.Hour).Format(time.RFC3339)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/logs?from="+from+"&to="+to, sess, "")
	var resp struct {
		Data  []model.Log `json:"data"`
		Total int64       `json:"total"`
	}
	decodeJSON(t, rec, &resp)
	if resp.Total != 2 {
		t.Fatalf("date range: total=%d (expected 2)", resp.Total)
	}
}

func TestAdmin_AnalyticsTimeSeries(t *testing.T) {
	app := testhelper.New(t)
	ch := app.AddChannel("c", "openai", "https://x", []string{"a"}, "sk-key")
	tok := app.AddToken("sk-t", "t")

	base := time.Now().UTC().Truncate(time.Hour)
	for i, h := range []time.Duration{-2, 0, 1} {
		l := &model.Log{
			TokenID: tok.ID, ChannelID: ch.ID, KeyID: 1, Model: "a",
			PromptTokens: 10, CompletionTokens: 5, RealCostUSD: 0.001,
			BilledCostUSD: 0.001, StatusCode: 200, CreatedAt: base.Add(h * time.Hour),
		}
		_ = i
		_ = app.Store.CreateLog(l)
	}
	sess := login(t, app)

	rec := do(t, app.Admin.Routes(), http.MethodGet, "/analytics/timeseries?bucket=3600", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("timeseries: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []store.SeriesPoint `json:"data"`
	}
	decodeJSON(t, rec, &resp)
	if len(resp.Data) == 0 {
		t.Fatal("expected at least one bucket")
	}
	total := int64(0)
	for _, p := range resp.Data {
		total += p.Requests
	}
	if total != 3 {
		t.Fatalf("timeseries total: %d", total)
	}
}

func TestAdmin_AnalyticsByModel(t *testing.T) {
	app := testhelper.New(t)
	ch := app.AddChannel("c", "openai", "https://x", []string{"a", "b"}, "sk-key")
	tok := app.AddToken("sk-t", "t")

	for i := 0; i < 3; i++ {
		_ = app.Store.CreateLog(&model.Log{TokenID: tok.ID, ChannelID: ch.ID, KeyID: 1, Model: "a", StatusCode: 200})
	}
	for i := 0; i < 2; i++ {
		_ = app.Store.CreateLog(&model.Log{TokenID: tok.ID, ChannelID: ch.ID, KeyID: 1, Model: "b", StatusCode: 200})
	}
	sess := login(t, app)

	rec := do(t, app.Admin.Routes(), http.MethodGet, "/analytics/by-model", sess, "")
	var resp struct {
		Data []store.NamedMetric `json:"data"`
	}
	decodeJSON(t, rec, &resp)
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 models, got %d", len(resp.Data))
	}
	if resp.Data[0].Label != "a" || resp.Data[0].Count != 3 {
		t.Fatalf("expected a=3 first, got %+v", resp.Data[0])
	}
}

func TestAdmin_AnalyticsByChannel(t *testing.T) {
	app := testhelper.New(t)
	ch1 := app.AddChannel("c1", "openai", "https://x", []string{"a"}, "sk-1")
	ch2 := app.AddChannel("c2", "openai", "https://y", []string{"a"}, "sk-2")
	tok := app.AddToken("sk-t", "t")

	for i := 0; i < 2; i++ {
		_ = app.Store.CreateLog(&model.Log{TokenID: tok.ID, ChannelID: ch1.ID, KeyID: 1, Model: "a", StatusCode: 200})
	}
	_ = app.Store.CreateLog(&model.Log{TokenID: tok.ID, ChannelID: ch2.ID, KeyID: 1, Model: "a", StatusCode: 200})

	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/analytics/by-channel", sess, "")
	var resp struct {
		Data []store.NamedMetric `json:"data"`
	}
	decodeJSON(t, rec, &resp)
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(resp.Data))
	}
	if resp.Data[0].Count != 2 {
		t.Fatalf("expected ch1=2 first, got %+v", resp.Data[0])
	}
}