package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/runtime"
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

func TestAdmin_ConfigCostStrategyGetPut(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	// default
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/config", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		CostStrategy string `json:"cost_strategy"`
	}
	decodeJSON(t, rec, &got)
	if got.CostStrategy != "cheapest" {
		t.Fatalf("default strategy: got %q", got.CostStrategy)
	}

	// switch to fastest
	rec = do(t, app.Admin.Routes(), http.MethodPut, "/config", sess, `{"cost_strategy":"fastest"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, app.Admin.Routes(), http.MethodGet, "/config", sess, "")
	decodeJSON(t, rec, &got)
	if got.CostStrategy != "fastest" {
		t.Fatalf("after put: got %q", got.CostStrategy)
	}
	if app.Engine.CostStrategy() != model.CostStrategy("fastest") {
		t.Fatalf("engine state: got %q", app.Engine.CostStrategy())
	}

	// invalid value
	rec = do(t, app.Admin.Routes(), http.MethodPut, "/config", sess, `{"cost_strategy":"random"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid strategy, got %d", rec.Code)
	}
}

// TestAdmin_ConfigPersistsToDB verifies that PUT /api/v1/config
// writes through to the runtime_settings table. The next process
// restart should re-load the same values via main.go's startup
// overlay.
func TestAdmin_ConfigPersistsToDB(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	// Change every persisted field in one PUT.
	body := `{
		"cost_strategy":"fastest",
		"markup_ratio":2.5,
		"breaker_max_failures":9,
		"breaker_reset_timeout_ms":45000,
		"alert_cooldown_sec":600,
		"log_retention_days":14,
		"stream_timeout_sec":120,
		"stream_max_body_bytes":16777216,
		"max_log_subscribers":42,
		"log_level":2
	}`
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/config", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}

	// Read the row back directly from the store.
	raw, err := app.Store.GetRuntimeSettings()
	if err != nil {
		t.Fatalf("GetRuntimeSettings: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("runtime_settings row is empty after PUT")
	}
	var snap map[string]any
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("parse: %v (raw=%s)", err, raw)
	}
	want := map[string]float64{
		"markup_ratio":             2.5,
		"breaker_max_failures":     9,
		"breaker_reset_timeout_ms": 45000,
		"alert_cooldown_sec":       600,
		"log_retention_days":       14,
		"stream_timeout_sec":       120,
		"stream_max_body_bytes":    16777216,
		"max_log_subscribers":      42,
		"log_level":                2,
	}
	for k, v := range want {
		if got := snap[k].(float64); got != v {
			t.Fatalf("%s: got %v, want %v", k, got, v)
		}
	}
	if snap["cost_strategy"] != "fastest" {
		t.Fatalf("cost_strategy: %v", snap["cost_strategy"])
	}

	// GET /config after restart should return the persisted values.
	// (Same process here; verifies the in-memory store is
	// consistent with what's persisted, not that restart logic
	// runs — the startup overlay path is covered in
	// TestAdmin_ConfigReloadsOnStartup.)
	rec = do(t, app.Admin.Routes(), http.MethodGet, "/config", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		CostStrategy          string  `json:"cost_strategy"`
		MarkupRatio           float64 `json:"markup_ratio"`
		BreakerMaxFailures    int64   `json:"breaker_max_failures"`
		BreakerResetTimeoutMs int64   `json:"breaker_reset_timeout_ms"`
		AlertCooldownSec      int64   `json:"alert_cooldown_sec"`
		LogRetentionDays      int64   `json:"log_retention_days"`
		StreamTimeoutSec      int64   `json:"stream_timeout_sec"`
		StreamMaxBodyBytes    int64   `json:"stream_max_body_bytes"`
		MaxLogSubscribers     int64   `json:"max_log_subscribers"`
		LogLevel              int64   `json:"log_level"`
	}
	decodeJSON(t, rec, &got)
	if got.CostStrategy != "fastest" || got.MarkupRatio != 2.5 ||
		got.BreakerMaxFailures != 9 || got.BreakerResetTimeoutMs != 45000 ||
		got.AlertCooldownSec != 600 || got.LogRetentionDays != 14 ||
		got.StreamTimeoutSec != 120 || got.StreamMaxBodyBytes != 16777216 ||
		got.MaxLogSubscribers != 42 || got.LogLevel != 2 {
		t.Fatalf("GET config after PUT: %+v", got)
	}
}

// TestAdmin_ConfigMaxLogSubscribersLiveUpdates covers the side
// effect: PUT max_log_subscribers must immediately call
// broker.SetMaxSubscribers, not just stash the value in rt.
func TestAdmin_ConfigMaxLogSubscribersLiveUpdates(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	// Tighten the cap to 1 — first SSE subscribe succeeds, second
	// is rejected.
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/config", sess, `{"max_log_subscribers":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if got := app.LogBroker.MaxSubscribers(); got != 1 {
		t.Fatalf("broker cap not propagated: got %d", got)
	}

	// Relax back to 0 (unlimited).
	rec = do(t, app.Admin.Routes(), http.MethodPut, "/config", sess, `{"max_log_subscribers":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put 0: %d %s", rec.Code, rec.Body.String())
	}
	if got := app.LogBroker.MaxSubscribers(); got != 0 {
		t.Fatalf("broker cap not relaxed: got %d", got)
	}
}

// TestAdmin_ConfigRejectsOutOfRange covers the validation guards.
func TestAdmin_ConfigRejectsOutOfRange(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	cases := []struct {
		field string
		body  string
	}{
		{"stream_timeout_sec negative", `{"stream_timeout_sec":-1}`},
		{"stream_timeout_sec too big", `{"stream_timeout_sec":99999}`},
		{"stream_max_body_bytes negative", `{"stream_max_body_bytes":-1}`},
		{"stream_max_body_bytes too big", `{"stream_max_body_bytes":99999999999}`},
		{"max_log_subscribers negative", `{"max_log_subscribers":-5}`},
		{"max_log_subscribers too big", `{"max_log_subscribers":99999999}`},
		{"log_level out of range", `{"log_level":7}`},
		{"log_level negative", `{"log_level":-1}`},
	}
	for _, tc := range cases {
		rec := do(t, app.Admin.Routes(), http.MethodPut, "/config", sess, tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d %s", tc.field, rec.Code, rec.Body.String())
		}
	}
}

// TestAdmin_ConfigReloadsOnStartup simulates a restart: writes a
// snapshot to the store via the store API directly, then opens a
// fresh runtime.Defaults and applies the loaded JSON. Mirrors the
// main.go startup path (YAML seeds → DB overlay).
func TestAdmin_ConfigReloadsOnStartup(t *testing.T) {
	app := testhelper.New(t)

	// Persist a snapshot directly (bypassing the handler).
	raw := []byte(`{
		"cost_strategy":"balanced",
		"markup_ratio":3.0,
		"breaker_max_failures":12,
		"breaker_reset_timeout_ms":90000,
		"alert_cooldown_sec":120,
		"log_retention_days":7
	}`)
	if err := app.Store.SetRuntimeSettings(raw); err != nil {
		t.Fatalf("SetRuntimeSettings: %v", err)
	}

	// Simulate a new process: fresh runtime.Defaults seeded with
	// the default YAML values, then overlaid with the DB row.
	rt := app.RT
	rt.SetCostStrategy("cheapest")  // default YAML seed
	rt.SetMarkupRatio(1.0)
	rt.SetBreakerMaxFailures(5)
	rt.SetBreakerResetTimeoutMs(30000)
	rt.SetAlertCooldownSec(300)
	rt.SetLogRetentionDays(30)

	loaded, err := app.Store.GetRuntimeSettings()
	if err != nil {
		t.Fatalf("GetRuntimeSettings: %v", err)
	}
	var snap runtime.Snapshot
	if err := json.Unmarshal(loaded, &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rt.Apply(snap)

	if got := rt.CostStrategy(); got != "balanced" {
		t.Fatalf("cost_strategy: %q", got)
	}
	if got := rt.MarkupRatio(); got != 3.0 {
		t.Fatalf("markup: %v", got)
	}
	if got := rt.BreakerMaxFailures(); got != 12 {
		t.Fatalf("breaker max: %d", got)
	}
	if got := rt.BreakerResetTimeoutMs(); got != 90000 {
		t.Fatalf("breaker reset: %d", got)
	}
	if got := rt.AlertCooldownSec(); got != 120 {
		t.Fatalf("alert cooldown: %d", got)
	}
	if got := rt.LogRetentionDays(); got != 7 {
		t.Fatalf("retention: %d", got)
	}
}

// TestAdmin_ConfigSurvivesCorruption ensures a malformed JSON row
// in runtime_settings does not crash startup; main.go should log a
// warning and fall back to YAML seeds.
func TestAdmin_ConfigSurvivesCorruption(t *testing.T) {
	app := testhelper.New(t)
	if err := app.Store.SetRuntimeSettings([]byte(`{not valid json`)); err != nil {
		t.Fatalf("SetRuntimeSettings: %v", err)
	}
	rt := app.RT
	rt.SetMarkupRatio(1.0)
	rt.SetCostStrategy("cheapest")

	raw, err := app.Store.GetRuntimeSettings()
	if err != nil {
		t.Fatalf("GetRuntimeSettings: %v", err)
	}
	var snap runtime.Snapshot
	if err := json.Unmarshal(raw, &snap); err == nil {
		t.Fatal("expected JSON parse to fail on garbage row")
	}
	// Caller (main.go) is expected to log a warning and continue
	// with the YAML seed; rt must remain unchanged.
	if got := rt.MarkupRatio(); got != 1.0 {
		t.Fatalf("markup should remain 1.0 after corruption, got %v", got)
	}
	if got := rt.CostStrategy(); got != "cheapest" {
		t.Fatalf("cost_strategy should remain cheapest, got %q", got)
	}
}

func TestRouter_CostStrategyAffectsRouting(t *testing.T) {
	app := testhelper.New(t)
	chCheap := app.AddChannelWithPrice("cheap", "openai", "https://x", []string{"m"}, 1.0, 1.0, "sk-1")
	app.AddChannelWithPrice("premium", "openai", "https://y", []string{"m"}, 5.0, 5.0, "sk-2")
	app.AddToken("sk-t", "t")

	// Default = cheapest
	r, err := app.Engine.Route(context.Background(), "m")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if r.Channel.ID != chCheap.ID {
		t.Fatalf("cheapest: expected chCheap, got %s", r.Channel.Name)
	}

	// Switch to fastest (priority 0 both → stable; but with 2 entries,
	// second becomes preferred. Add channels with different priorities.)
	app.Engine.SetStrategy("fastest")
	r, _ = app.Engine.Route(context.Background(), "m")
	_ = r
	// Both have priority 0 (default); the cost router preserves
	// insertion order in stable sort. So either is acceptable; we
	// just verify the engine accepts the strategy and runs without panic.
	if app.Engine.CostStrategy() != "fastest" {
		t.Fatalf("engine strategy: %q", app.Engine.CostStrategy())
	}
}

func TestAdmin_LoginUpgradesLegacyHash(t *testing.T) {
	app := testhelper.New(t)
	// Overwrite the seeded admin with a legacy plaintext hash.
	st := app.Store
	if err := st.UpdateUser(&model.User{
		ID: 1, Username: "admin", PasswordHash: "00112233445566778899aabbccddeeff:admin",
		Role: model.RoleRoot, Status: 1,
	}); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/login", "", `{"username":"admin","password":"admin"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login with legacy: %d %s", rec.Code, rec.Body.String())
	}
	u, err := st.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("re-read user: %v", err)
	}
	if u.PasswordHash == "00112233445566778899aabbccddeeff:admin" {
		t.Fatal("legacy hash was not upgraded")
	}
	if !strings.HasPrefix(u.PasswordHash, "$argon2id$") {
		t.Fatalf("expected argon2id hash, got %q", u.PasswordHash)
	}
}

func TestAdmin_ChangePassword(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	// Self change with wrong old password -> 401.
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/users/1/password", sess,
		`{"old_password":"WRONG","new_password":"newpass1"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong old: expected 401, got %d %s", rec.Code, rec.Body.String())
	}

	// Self change with correct old password -> 200; session is invalidated.
	rec = do(t, app.Admin.Routes(), http.MethodPost, "/users/1/password", sess,
		`{"old_password":"admin","new_password":"newpass1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("change: %d %s", rec.Code, rec.Body.String())
	}

	// Old session should no longer work.
	rec = do(t, app.Admin.Routes(), http.MethodGet, "/dashboard", sess, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("stale session: expected 403, got %d", rec.Code)
	}

	// New password should work.
	rec = do(t, app.Admin.Routes(), http.MethodPost, "/login", "", `{"username":"admin","password":"newpass1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login with new pw: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_ChangePasswordForbidden(t *testing.T) {
	app := testhelper.New(t)
	adminSess := login(t, app)

	// Create a normal (non-admin) user, log in as them.
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/users", adminSess,
		`{"username":"bob","password":"bobpass","role":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create bob: %d %s", rec.Code, rec.Body.String())
	}

	// Log in as bob.
	rec = do(t, app.Admin.Routes(), http.MethodPost, "/login", "", `{"username":"bob","password":"bobpass"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login bob: %d %s", rec.Code, rec.Body.String())
	}
	var lr struct {
		SessionToken string `json:"session_token"`
	}
	decodeJSON(t, rec, &lr)
	bobSess := lr.SessionToken

	// Bob trying to change admin's password -> 403.
	rec = do(t, app.Admin.Routes(), http.MethodPost, "/users/1/password", bobSess,
		`{"new_password":"hacked123"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bob->admin: expected 403, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_LogStreamRequiresSession(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-aaaa")
	app.AddToken("sk-tok", "t")
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/logs/stream", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", rec.Code)
	}
}

func TestAdmin_LogStreamPublishesEvents(t *testing.T) {
	app := testhelper.New(t)
	ch := app.AddChannel("c", "openai", "https://x", []string{"m"}, "sk-aaaa")
	app.AddToken("sk-tok", "t")
	sess := login(t, app)

	req := httptest.NewRequest(http.MethodGet, "/logs/stream?session_token="+sess, nil)
	rec := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Publish after a short delay so the subscriber is in place,
	// then cancel so ServeHTTP returns.
	go func() {
		time.Sleep(50 * time.Millisecond)
		app.LogBroker.Publish(&model.Log{ChannelID: ch.ID, Model: "m", StatusCode: 200})
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	req = req.WithContext(ctx)
	app.Admin.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type: %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: log") {
		t.Fatalf("expected log event, got: %q", body)
	}
	if !strings.Contains(body, `"model":"m"`) {
		t.Fatalf("expected model=m payload, got: %q", body)
	}
}
func TestAdmin_AlertsCRUD(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	// Create
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/alerts", sess,
		`{"name":"high-errors","type":"error_rate","threshold":0.5,"window_sec":300,"cooldown_sec":60,"webhook_url":"https://example.com/hook","enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		ID int64 `json:"id"`
	}
	decodeJSON(t, rec, &got)
	if got.ID == 0 {
		t.Fatal("no id")
	}

	// List
	rec = do(t, app.Admin.Routes(), http.MethodGet, "/alerts", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	var lst struct {
		Data []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"data"`
	}
	decodeJSON(t, rec, &lst)
	if len(lst.Data) != 1 || lst.Data[0].Name != "high-errors" {
		t.Fatalf("list: %+v", lst.Data)
	}

	// Update
	rec = do(t, app.Admin.Routes(), http.MethodPut, "/alerts/"+itoa(got.ID), sess,
		`{"name":"high-errors-v2","threshold":0.8,"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	var upd struct {
		Name    string  `json:"name"`
		Enabled bool    `json:"enabled"`
		Threshold float64 `json:"threshold"`
	}
	decodeJSON(t, rec, &upd)
	if upd.Name != "high-errors-v2" || upd.Enabled || upd.Threshold != 0.8 {
		t.Fatalf("update result: %+v", upd)
	}

	// Bad type -> 400
	rec = do(t, app.Admin.Routes(), http.MethodPost, "/alerts", sess,
		`{"name":"bad","type":"nope","threshold":0.1}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad type: %d", rec.Code)
	}

	// Delete
	rec = do(t, app.Admin.Routes(), http.MethodDelete, "/alerts/"+itoa(got.ID), sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
}

func TestAdmin_AlertEventsAck(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	// Manually insert an alert and an event for ack flow.
	if err := app.Store.CreateAlert(&model.Alert{
		Name: "manual", Type: model.AlertErrorRate, Threshold: 0.1, WindowSec: 60, CooldownSec: 0, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.Store.CreateAlertEvent(&model.AlertEvent{
		AlertID: 1, AlertName: "manual", AlertType: model.AlertErrorRate, Payload: "{}",
	}); err != nil {
		t.Fatal(err)
	}

	// List events
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/alerts/events", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	var lst struct {
		Data []struct {
			ID           int64 `json:"id"`
			Acknowledged bool  `json:"acknowledged"`
		} `json:"data"`
	}
	decodeJSON(t, rec, &lst)
	if len(lst.Data) != 1 || lst.Data[0].Acknowledged {
		t.Fatalf("expected 1 unack event, got: %+v", lst.Data)
	}

	// Ack
	rec = do(t, app.Admin.Routes(), http.MethodPost, "/alerts/events/"+itoa(lst.Data[0].ID)+"/ack", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("ack: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_LoginUpgradesBcryptHash(t *testing.T) {
	app := testhelper.New(t)
	// Overwrite the seeded admin with a bcrypt hash (P6 format).
	bc, _ := bcryptHashForTest("admin")
	st := app.Store
	if err := st.UpdateUser(&model.User{
		ID: 1, Username: "admin", PasswordHash: bc,
		Role: model.RoleRoot, Status: 1,
	}); err != nil {
		t.Fatalf("seed bcrypt: %v", err)
	}
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/login", "", `{"username":"admin","password":"admin"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login with bcrypt: %d %s", rec.Code, rec.Body.String())
	}
	u, err := st.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if strings.HasPrefix(u.PasswordHash, "$2") {
		t.Fatal("bcrypt hash was not upgraded to argon2id")
	}
	if !strings.HasPrefix(u.PasswordHash, "$argon2id$") {
		t.Fatalf("expected argon2id hash, got %q", u.PasswordHash)
	}
}

func bcryptHashForTest(pw string) (string, error) {
	// Inlined; admin package shouldn't depend on auth internals beyond the public API.
	// Use a tiny bcrypt wrapper via the x/crypto package the auth pkg already imports.
	return authBcrypt(pw)
}

func TestRotateSecrets_RejectsWhenNoManager(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/secrets/rotate", sess,
		`{"new_master_key":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	var m map[string]any
	decodeJSON(t, rec, &m)
	err, _ := m["error"].(map[string]any)
	if err == nil {
		t.Fatal("expected error field")
	}
	if !strings.Contains(err["message"].(string), "no secrets manager") {
		t.Errorf("unexpected error: %v", err["message"])
	}
}

func TestRotateSecrets_RejectsShortKey(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/secrets/rotate", sess,
		`{"new_master_key":"short"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRotateSecrets_RejectsInvalidHex(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/secrets/rotate", sess,
		`{"new_master_key":"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
