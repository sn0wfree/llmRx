package webui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/model"
)

// renderMobile renders a user-center page (role < 10) through the base
// layout with demo data; extra overrides/extends the data map.
func renderMobile(t *testing.T, body string, extra map[string]any) string {
	t.Helper()
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	data := map[string]any{
		"Body":   body,
		"Title":  "测试 · llmRx",
		"User":   &User{ID: 7, Username: "alice", Role: 1},
		"Me":     &meView{Username: "alice", APIKey: "sk-rx-demo", BudgetUSD: 50, UsedUSD: 10, RemainingUSD: 40},
		"Active": "me",
	}
	for k, v := range extra {
		data[k] = v
	}
	var buf bytes.Buffer
	if err := r.Render(&buf, body, data); err != nil {
		t.Fatalf("render %s: %v", body, err)
	}
	return buf.String()
}

// renderAdmin renders the same page for an admin (role >= 10) so tests
// can assert the desktop-only branch.
func renderAdmin(t *testing.T, body string) string {
	t.Helper()
	return renderMobile(t, body, map[string]any{
		"User":   &User{ID: 7, Username: "alice", Role: 100},
		"Active": "",
	})
}

// dockSegment extracts the mobile bottom dock markup from a page,
// starting at the <nav> opening tag so its classes are included.
func dockSegment(out string) string {
	i := strings.Index(out, `aria-label="页面导航"`)
	if i < 0 {
		return ""
	}
	k := strings.LastIndex(out[:i], "<nav")
	if k < 0 {
		return ""
	}
	j := strings.Index(out[i:], "</nav>")
	if j < 0 {
		return ""
	}
	return out[k : i+j]
}

// tabIsActive reports whether the dock tab for href carries the active
// (highlighted) classes.
func tabIsActive(seg, href string) bool {
	k := strings.Index(seg, `href="`+href+`"`)
	if k < 0 {
		return false
	}
	end := strings.Index(seg[k:], "</a>")
	if end < 0 {
		return false
	}
	return strings.Contains(seg[k:k+end], "bg-blue-600")
}

// TestMobileUserCenterChrome: the user center (role < 10) renders the
// mobile chrome (top bar + bottom dock + gradient balance hero), while
// admin (role >= 10) keeps the desktop-only layout with no dock.
func TestMobileUserCenterChrome(t *testing.T) {
	user := renderMobile(t, "me_body", nil)
	for _, want := range []string{
		`aria-label="页面导航"`,           // bottom dock
		`<header class="md:hidden`,    // mobile top bar
		"from-blue-600 to-indigo-700", // gradient balance hero
		`href="/account/topup"`,       // dock link
		`href="/account/usage"`,       // dock link
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user center render missing %q", want)
		}
	}

	admin := renderAdmin(t, "me_body")
	if strings.Contains(admin, `aria-label="页面导航"`) {
		t.Error("admin render should not contain the mobile dock")
	}
	if strings.Contains(admin, `<header class="md:hidden`) {
		t.Error("admin render should not contain the mobile top bar")
	}
}

// TestMobileDockActiveState: exactly the dock tab matching Active is
// highlighted; an unknown Active highlights nothing.
func TestMobileDockActiveState(t *testing.T) {
	cases := []struct {
		active string
		want   string // highlighted tab href
	}{
		{"me", "/account"},
		{"me-usage", "/account/usage"},
		{"me-topup", "/account/topup"},
		{"", ""},
	}
	hrefs := []string{"/account", "/account/usage", "/account/topup"}
	for _, tc := range cases {
		out := renderMobile(t, "me_body", map[string]any{"Active": tc.active})
		seg := dockSegment(out)
		if seg == "" {
			t.Fatalf("Active %q: dock not found in output", tc.active)
		}
		for _, href := range hrefs {
			want := href == tc.want
			if got := tabIsActive(seg, href); got != want {
				t.Errorf("Active %q: tab %s active = %v, want %v", tc.active, href, got, want)
			}
		}
		if !strings.Contains(seg, "md:hidden") {
			t.Errorf("Active %q: dock should stay mobile-only (md:hidden)", tc.active)
		}
	}
}

// TestMobileTopBarLinks: the mobile top bar links to the password form
// of the signed-in user and posts the logout form.
func TestMobileTopBarLinks(t *testing.T) {
	out := renderMobile(t, "me_body", map[string]any{
		"User": &User{ID: 42, Username: "alice", Role: 1},
	})
	if !strings.Contains(out, `href="/admin/users/42/password"`) {
		t.Error("top bar missing password link for user id 42")
	}
	if !strings.Contains(out, `action="/admin/logout"`) {
		t.Error("top bar missing logout form")
	}
	if !strings.Contains(out, `aria-label="登出"`) {
		t.Error("top bar logout button should be labeled")
	}
}

// TestBalanceHeroProgress: the hero shows a usage progress bar when a
// budget is set, and omits it without one.
func TestBalanceHeroProgress(t *testing.T) {
	out := renderMobile(t, "me_body", map[string]any{
		"Me": &meView{Username: "alice", APIKey: "sk-rx-demo", BudgetUSD: 50, UsedUSD: 16, RemainingUSD: 34},
	})
	if !strings.Contains(out, `width: 32.0%`) {
		t.Error("hero progress bar should show 32.0%% used")
	}
	if !strings.Contains(out, "预算 $50.00 · 已用 $16.0000") {
		t.Error("hero should show budget/used caption")
	}

	out = renderMobile(t, "me_body", map[string]any{
		"Me": &meView{Username: "alice", APIKey: "sk-rx-demo", BudgetUSD: 0, UsedUSD: 0, RemainingUSD: 0},
	})
	if strings.Contains(out, `bg-white/25 rounded-full`) {
		t.Error("hero should not render a progress bar without a budget")
	}
}

// TestMobileLowBalanceWarn: the hero warn strip shows when remaining
// budget is under $1 or usage reaches 90%, and stays hidden otherwise.
func TestMobileLowBalanceWarn(t *testing.T) {
	cases := []struct {
		budget, used float64
		wantWarn     bool
	}{
		{50, 46.5, true}, // 93% used
		{50, 49.5, true}, // remaining < $1
		{50, 10, false},  // healthy
		{0, 0, true},     // no budget at all
	}
	for _, tc := range cases {
		out := renderMobile(t, "me_body", map[string]any{
			"Me": &meView{Username: "alice", APIKey: "sk-rx-demo", BudgetUSD: tc.budget, UsedUSD: tc.used, RemainingUSD: tc.budget - tc.used},
		})
		got := strings.Contains(out, "余额偏低")
		if got != tc.wantWarn {
			t.Errorf("budget=%.2f used=%.2f: warn = %v, want %v", tc.budget, tc.used, got, tc.wantWarn)
		}
	}
}

// sampleLogs builds the recent-activity fixture: 4xx rows must get a
// red status dot on mobile, 2xx a green one.
func sampleLogs() []model.Log {
	base := time.Date(2026, 11, 11, 14, 32, 0, 0, time.UTC)
	return []model.Log{
		{Model: "claude-sonnet-4-5", CreatedAt: base, PromptTokens: 6421, CompletionTokens: 2000, BilledCostUSD: 0.0291, StatusCode: 200},
		{Model: "claude-opus-4", CreatedAt: base.Add(-time.Minute), PromptTokens: 4320, CompletionTokens: 1000, BilledCostUSD: 0.201, StatusCode: 429},
	}
}

// TestRecentActivityDualRender: recent activity renders the desktop
// table and the mobile list; the 429 row gets a red status dot.
func TestRecentActivityDualRender(t *testing.T) {
	out := renderMobile(t, "me_body", map[string]any{"RecentLogs": sampleLogs()})
	for _, want := range []string{
		`hidden md:block`, // desktop table wrapper
		`md:hidden`,       // mobile list
		"claude-sonnet-4-5",
		"bg-red-500",   // 429 dot
		"bg-green-500", // 200 dot
	} {
		if !strings.Contains(out, want) {
			t.Errorf("recent activity missing %q", want)
		}
	}

	out = renderMobile(t, "me_body", nil) // no logs
	if !strings.Contains(out, "暂无请求记录") {
		t.Error("empty recent activity should show the placeholder")
	}
}

// TestUsagePageModelListAndChartScript: the usage page renders the
// per-model stats as a desktop table plus a mobile list, and the chart
// script sizes the canvas via the element (a regression guard — sizing
// it on the 2D context silently never drew the chart).
func TestUsagePageModelListAndChartScript(t *testing.T) {
	out := renderMobile(t, "me_usage_body", map[string]any{
		"Range":   "7d",
		"Summary": usageSummary{Requests: 312, PromptTokens: 892300, CompletionTokens: 214550, CacheHits: 84, BilledCostUSD: 12.4062},
		"ByModel": []logstore.NamedMetric{
			{Label: "claude-sonnet-4-5", Count: 486, Tokens: 1420000, Cost: 21.318},
			{Label: "gpt-4o-mini", Count: 512, Tokens: 620000, Cost: 2.412},
		},
		"SeriesJSON": `[{"bucket":1,"billed_cost_usd":0.5},{"bucket":2,"billed_cost_usd":0.4}]`,
	})
	for _, want := range []string{
		"按模型统计",
		`hidden md:block`,
		`md:hidden`,
		"claude-sonnet-4-5",
		"486 次 · 1420000 tok",
		// chart script regression guard: size the canvas element itself
		"el.width = (el.clientWidth||600) * dpr",
		"el.height = 200 * dpr",
		"el.style.width = (el.clientWidth||600) + 'px'",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("usage page missing %q", want)
		}
	}
	if strings.Contains(out, "canvas.width = (el.clientWidth") {
		t.Error("chart script must not size the canvas via the 2D context")
	}
}

// TestTopupStackedForm: the redeem form stacks on mobile and keeps the
// inline layout from sm up; the page highlights the topup dock tab.
func TestTopupStackedForm(t *testing.T) {
	out := renderMobile(t, "me_topup_body", map[string]any{"Active": "me-topup"})
	if !strings.Contains(out, `flex flex-col sm:flex-row`) {
		t.Error("redeem form should stack on mobile (flex-col sm:flex-row)")
	}
	if !strings.Contains(out, "RXM-XXXXXXXX") {
		t.Error("redeem form should show the code format hint")
	}
	seg := dockSegment(out)
	if !tabIsActive(seg, "/account/topup") {
		t.Error("topup tab should be highlighted on the topup page")
	}
}

// TestAccountEmptyStateNoAPIKey: without an API key the hero and
// credentials sections are replaced by the empty-state card.
func TestAccountEmptyStateNoAPIKey(t *testing.T) {
	out := renderMobile(t, "me_body", map[string]any{
		"Me": &meView{Username: "alice"},
	})
	if strings.Contains(out, "from-blue-600 to-indigo-700") {
		t.Error("empty state should not render the gradient hero")
	}
	if !strings.Contains(out, "尚未分配 API Key") {
		t.Error("empty state card missing")
	}
}

// asideSegment extracts the <aside ...> opening tag of the sidebar.
func asideSegment(out string) string {
	i := strings.Index(out, "<aside")
	if i < 0 {
		return ""
	}
	j := strings.Index(out[i:], ">")
	if j < 0 {
		return ""
	}
	return out[i : i+j+1]
}

// TestSidebarBranchClasses: the sidebar hides below md for user-center
// users and stays always-visible for admins.
func TestSidebarBranchClasses(t *testing.T) {
	user := asideSegment(renderMobile(t, "me_body", nil))
	if !strings.Contains(user, "hidden md:flex") {
		t.Error("user sidebar should carry hidden md:flex")
	}

	admin := asideSegment(renderAdmin(t, "me_body"))
	if admin == "" {
		t.Fatal("admin sidebar missing")
	}
	if strings.Contains(admin, "hidden") {
		t.Error("admin sidebar should not be hidden on mobile")
	}
}

// TestUsagePageChartAndSummaryHiddenWithoutData: with no series the
// chart card and its script are skipped entirely; with no TodaySummary
// the stats card is skipped too.
func TestUsagePageChartAndSummaryHiddenWithoutData(t *testing.T) {
	out := renderMobile(t, "me_usage_body", map[string]any{
		"Range":      "24h",
		"Summary":    usageSummary{Requests: 1},
		"SeriesJSON": "[]",
	})
	if strings.Contains(out, "花费趋势") {
		t.Error("chart card should be omitted without series data")
	}
	if strings.Contains(out, `id="usage-chart"`) {
		t.Error("chart canvas should be omitted without series data")
	}
	if !strings.Contains(out, "用量统计") {
		t.Error("usage page title should still render")
	}

	out = renderMobile(t, "me_body", map[string]any{
		"Active": "me",
	})
	if strings.Contains(out, "今日统计") {
		t.Error("today stats card should be omitted without TodaySummary")
	}
}
