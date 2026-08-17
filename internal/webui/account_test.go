package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/auth"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

// --- helpers ---------------------------------------------------------------

// seedUserWithPlan creates a non-admin user, a plan with the given budget,
// and a token named after the user that links the plan. Returns the user
// (with session cookie) so the caller can drive requests.
func seedUserWithPlan(tb testing.TB, h *Handler, st WebuiStore, name string, role model.UserRole, budget, usedUSD float64) (*model.User, string) {
	tb.Helper()
	hash, err := auth.Hash("pw-" + name)
	if err != nil {
		tb.Fatalf("auth.Hash: %v", err)
	}
	u := &model.User{Username: name, PasswordHash: hash, Role: role, Status: 1}
	if err := st.CreateUser(u); err != nil {
		tb.Fatalf("create user %s: %v", name, err)
	}
	plan := &model.Plan{
		Name:      name + "-plan",
		BudgetUSD: budget,
		UsedUSD:   usedUSD,
		Status:    1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := st.CreatePlan(plan); err != nil {
		tb.Fatalf("create plan: %v", err)
	}
	tok := &model.Token{
		Name:      name,
		Key:       "sk-test-" + name,
		PlanID:    plan.ID,
		UserID:    u.ID,
		Status:    model.TokenActive,
		CreatedAt: time.Now(),
	}
	if err := st.CreateToken(tok); err != nil {
		tb.Fatalf("create token: %v", err)
	}
	// sessionCookieFor needs the full store.Store interface for
	// BackfillTokenUserIDs; the test harness's WebuiStore is a
	// structural subset of that interface and satisfies it.
	cookie := sessionCookieFor(tb, st.(store.Store), u)
	return u, cookie
}

// --- UserRoutes: registration ----------------------------------------------

// TestUserRoutes_RegistersAllPaths verifies the four expected self-service
// paths are wired on the UserRoutes() handler so a regression in route
// registration is caught immediately.
func TestUserRoutes_RegistersAllPaths(t *testing.T) {
	h, _ := newTestWebUI(t)
	_, cookie := seedUserWithPlan(t, h, h.store, "alice", model.RoleUser, 50, 0)

	cases := []struct {
		method, path string
		wantStatus   int
	}{
		{http.MethodGet, "/", http.StatusOK},
		{http.MethodGet, "/usage", http.StatusOK},
		{http.MethodGet, "/topup", http.StatusOK},
		{http.MethodGet, "/export.csv", http.StatusOK},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
		rec := httptest.NewRecorder()
		h.UserRoutes().ServeHTTP(rec, req)
		if rec.Code != tc.wantStatus {
			t.Errorf("%s %s: code=%d, want %d (body=%s)", tc.method, tc.path, rec.Code, tc.wantStatus, rec.Body.String())
		}
	}
}

// TestUserRoutes_RequiresAuth confirms an unauthenticated request to
// UserRoutes is rejected with 401/302 (redirect to login), not 200.
func TestUserRoutes_RequiresAuth(t *testing.T) {
	h, _ := newTestWebUI(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.UserRoutes().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Errorf("unauthenticated /account/ should not return 200, got %d", rec.Code)
	}
}

// TestUserRoutes_RejectsAdminOnly verifies that RoleAdmin still passes the
// RoleUser gate (admins are a superset). RoleRoot is excluded here to keep
// the assertion simple — the gate accepts everything >= RoleUser.
func TestUserRoutes_AdminAllowed(t *testing.T) {
	h, _ := newTestWebUI(t)
	_, cookie := newTestUser(t, h, h.store, withRole(model.RoleAdmin))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.UserRoutes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("RoleAdmin /account/: code=%d, want 200", rec.Code)
	}
}

// --- MePage rendering ------------------------------------------------------

// TestMePage_WelcomeAndSections verifies the enhanced home page renders all
// the new sections: welcome banner, balance card, today stats card,
// recent activity section, API credentials, quick-start curl block, and
// the 4-button action grid.
func TestMePage_WelcomeAndSections(t *testing.T) {
	h, _ := newTestWebUI(t)
	_, cookie := seedUserWithPlan(t, h, h.store, "bob", model.RoleUser, 50, 12.5)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.UserRoutes().ServeHTTP(rec, req)

	assertStatus(t, rec, http.StatusOK)
	body := rec.Body.String()

	want := []string{
		"欢迎回来，bob",       // welcome banner
		"账户余额",            // balance card
		"今日统计",            // today stats card
		"API 凭据",            // credentials card
		"快速开始",            // quick-start card
		"/v1/chat/completions", // curl example
		"复制命令",            // copy button on curl example
		"导出 CSV",            // action grid
		"登出",                // logout button
	}
	for _, w := range want {
		if !contains(body, w) {
			t.Errorf("MePage body missing %q", w)
		}
	}
}

// TestMePage_LowBalanceWarning verifies the yellow warning banner shows when
// remaining balance drops below $1.
func TestMePage_LowBalanceWarning(t *testing.T) {
	h, _ := newTestWebUI(t)
	// budget 1, used 0.50 → remaining 0.50 (< 1.0) → warning shown
	_, cookie := seedUserWithPlan(t, h, h.store, "lowbal", model.RoleUser, 1.0, 0.50)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.UserRoutes().ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusOK)

	if !contains(rec.Body.String(), "余额不足") {
		t.Error("low-balance warning banner should appear when remaining < $1")
	}
}

// TestMePage_NoLowBalanceWarning verifies the warning is suppressed when
// remaining balance is healthy.
func TestMePage_NoLowBalanceWarning(t *testing.T) {
	h, _ := newTestWebUI(t)
	// budget 100, used 10 → remaining 90 (>= 1.0) → no warning
	_, cookie := seedUserWithPlan(t, h, h.store, "healthy", model.RoleUser, 100, 10)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.UserRoutes().ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusOK)

	if contains(rec.Body.String(), "余额不足") {
		t.Error("low-balance warning should NOT appear when remaining >= $1")
	}
}

// TestMePage_RecentActivityRenders verifies the recent activity table renders
// a row for each log entry inserted against the user's token.
func TestMePage_RecentActivityRenders(t *testing.T) {
	h, st := newTestWebUI(t)
	_, cookie := seedUserWithPlan(t, h, st, "act", model.RoleUser, 50, 0)

	tok, _ := st.GetTokenByName("act")
	for i := 0; i < 3; i++ {
		if err := h.logStore.Insert(&model.Log{
			TokenID:          tok.ID,
			Model:            "gpt-4",
			PromptTokens:     10,
			CompletionTokens: 5,
			BilledCostUSD:    0.01,
			StatusCode:       200,
			CreatedAt:        time.Now(),
		}); err != nil {
			t.Fatalf("insert log: %v", err)
		}
	}
	// logStore.Insert is async; Flush() drains the batch into the driver
	// so Query() can see the rows before we render.
	h.logStore.Flush()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.UserRoutes().ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusOK)

	body := rec.Body.String()
	if !contains(body, "最近活动") {
		t.Fatal("recent activity section header missing")
	}
	// The model name should appear at least once in the rendered table.
	if !contains(body, "gpt-4") {
		t.Error("expected gpt-4 in recent activity rows")
	}
	// View-all link goes to /account/usage.
	if !contains(body, `href="/account/usage"`) {
		t.Error("expected link to /account/usage")
	}
}

// TestMePage_RecentActivityEmptyState verifies the empty-state copy renders
// when the user has no logs (rather than an empty <tbody>).
func TestMePage_RecentActivityEmptyState(t *testing.T) {
	h, _ := newTestWebUI(t)
	_, cookie := seedUserWithPlan(t, h, h.store, "empty", model.RoleUser, 50, 0)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.UserRoutes().ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusOK)

	if !contains(rec.Body.String(), "暂无请求记录") {
		t.Error("expected empty-state copy when no logs exist")
	}
}

// TestMePage_NoAPIKeyEmptyState verifies the friendly "no API key" panel
// renders when the user has no token (e.g. admin-created account).
func TestMePage_NoAPIKeyEmptyState(t *testing.T) {
	h, _ := newTestWebUI(t)
	// Plain user without a token / plan.
	_, cookie := newTestUser(t, h, h.store, withUsername("nokey"), withRole(model.RoleUser))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.UserRoutes().ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusOK)

	body := rec.Body.String()
	if !contains(body, "尚未分配 API Key") {
		t.Error("expected no-API-key empty-state panel")
	}
	// The quick-start / recent-activity sections should NOT appear when no key.
	if contains(body, "快速开始") {
		t.Error("quick-start section should be hidden when user has no API key")
	}
}

// TestMePage_PercentTextRenders verifies the budget progress bar shows the
// usage percentage. Used=10, Budget=100 → 10.0%.
func TestMePage_PercentTextRenders(t *testing.T) {
	h, _ := newTestWebUI(t)
	_, cookie := seedUserWithPlan(t, h, h.store, "pct", model.RoleUser, 100, 10)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.UserRoutes().ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusOK)

	if !contains(rec.Body.String(), "已使用 10.0%") {
		t.Errorf("expected '已使用 10.0%%' in body, got: %s", rec.Body.String())
	}
}

// TestMePage_StatusCodeBadgeColors checks that 2xx and 4xx log rows render
// with the expected CSS class hints (green vs red). This guards against the
// conditional branches in the template regressing.
func TestMePage_StatusCodeBadgeColors(t *testing.T) {
	h, st := newTestWebUI(t)
	_, cookie := seedUserWithPlan(t, h, st, "badge", model.RoleUser, 50, 0)
	tok, _ := st.GetTokenByName("badge")
	for _, code := range []int{200, 500} {
		if err := h.logStore.Insert(&model.Log{
			TokenID:    tok.ID,
			Model:      "gpt-4",
			StatusCode: code,
			CreatedAt:  time.Now(),
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	h.logStore.Flush()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.UserRoutes().ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusOK)

	body := rec.Body.String()
	if !contains(body, "bg-green-100") {
		t.Error("expected green badge class for 2xx status")
	}
	if !contains(body, "bg-red-100") {
		t.Error("expected red badge class for 5xx status")
	}
}

// --- MeUsagePage + ExportCSV ----------------------------------------------

// TestMeUsagePage_RendersForUser verifies the detailed usage page renders
// under /account/usage.
func TestMeUsagePage_RendersForUser(t *testing.T) {
	h, _ := newTestWebUI(t)
	_, cookie := seedUserWithPlan(t, h, h.store, "usage", model.RoleUser, 50, 5)

	req := httptest.NewRequest(http.MethodGet, "/usage", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.UserRoutes().ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusOK)

	if !contains(rec.Body.String(), "用量统计") {
		t.Error("usage page title missing")
	}
}

// TestMeUsagePage_RangeParam verifies the ?range=24h|7d|30d selector works.
func TestMeUsagePage_RangeParam(t *testing.T) {
	h, _ := newTestWebUI(t)
	_, cookie := seedUserWithPlan(t, h, h.store, "range", model.RoleUser, 50, 0)

	for _, r := range []string{"24h", "7d", "30d", ""} {
		path := "/usage"
		if r != "" {
			path += "?range=" + r
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
		rec := httptest.NewRecorder()
		h.UserRoutes().ServeHTTP(rec, req)
		assertStatus(t, rec, http.StatusOK)
	}
}

// TestMeUsageExportCSV_EmptyUser verifies a user with no logs gets a 400
// rather than an empty file, since there's nothing to export.
func TestMeUsageExportCSV_EmptyUser(t *testing.T) {
	h, _ := newTestWebUI(t)
	_, cookie := newTestUser(t, h, h.store, withUsername("no-logs"), withRole(model.RoleUser))

	req := httptest.NewRequest(http.MethodGet, "/export.csv", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.UserRoutes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty export, got %d", rec.Code)
	}
}

// TestMeUsageExportCSV_WithLogs verifies the export returns 200 + CSV
// content-type when logs exist.
func TestMeUsageExportCSV_WithLogs(t *testing.T) {
	h, st := newTestWebUI(t)
	_, cookie := seedUserWithPlan(t, h, st, "exp", model.RoleUser, 50, 0)
	tok, _ := st.GetTokenByName("exp")
	if err := h.logStore.Insert(&model.Log{
		TokenID:    tok.ID,
		Model:      "gpt-4",
		StatusCode: 200,
		CreatedAt:  time.Now(),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	h.logStore.Flush()

	req := httptest.NewRequest(http.MethodGet, "/export.csv", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.UserRoutes().ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}
	if !strings.Contains(rec.Body.String(), "gpt-4") {
		t.Error("CSV body should contain the model name")
	}
}

// --- MeTopupPage -----------------------------------------------------------

// TestMeTopupPage_Renders verifies the redemption-code page renders for a
// signed-in user with a token. The page should show current balance.
func TestMeTopupPage_Renders(t *testing.T) {
	h, _ := newTestWebUI(t)
	_, cookie := seedUserWithPlan(t, h, h.store, "top", model.RoleUser, 50, 0)

	req := httptest.NewRequest(http.MethodGet, "/topup", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.UserRoutes().ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusOK)

	body := rec.Body.String()
	if !contains(body, "充值") {
		t.Error("topup page title missing")
	}
	if !contains(body, "兑换") {
		t.Error("topup page should show the redemption form")
	}
}

// TestMeTopupPage_LinksToHome verifies the "← 返回首页" link uses the new
// /account path (regression test for the routing refactor).
func TestMeTopupPage_LinksToHome(t *testing.T) {
	h, _ := newTestWebUI(t)
	_, cookie := seedUserWithPlan(t, h, h.store, "link", model.RoleUser, 50, 0)

	req := httptest.NewRequest(http.MethodGet, "/topup", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.UserRoutes().ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusOK)

	if !contains(rec.Body.String(), `href="/account"`) {
		t.Errorf("topup back link should point to /account, body: %s", rec.Body.String())
	}
}

// --- Routes() should NOT expose /me anymore -------------------------------

// TestRoutes_NoLegacyMePath ensures the old /admin/me path was removed from
// the admin router when UserRoutes() was extracted. A regression here would
// mean admin users can still hit the legacy user page (duplicate route).
func TestRoutes_NoLegacyMePath(t *testing.T) {
	h, _ := newTestWebUI(t)
	_, cookie := newTestUser(t, h, h.store, withRole(model.RoleAdmin))

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Errorf("legacy /admin/me should not be reachable, got 200 (body=%s)", rec.Body.String())
	}
}

// TestLoginPage_RedirectsToAccount verifies the login template's JS redirect
// sends RoleUser to /account (not the legacy /admin/me).
func TestLoginPage_RedirectsToAccount(t *testing.T) {
	h, _ := newTestWebUI(t)
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusOK)

	if !contains(rec.Body.String(), "role >= 10 ? '/admin/dashboard' : '/account'") {
		t.Errorf("login redirect script should target /account for users, got: %s", rec.Body.String())
	}
}