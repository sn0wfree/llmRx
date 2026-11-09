package webui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/subusage"
	"github.com/sn0wfree/llmRx/internal/usage"
)

// TestSubscriptionsPage_Renders exercises the P22 page end to end:
// vendor report rendering, multi-window ordering, the experimental
// badge, the local-estimate footer, and the empty state. Template
// helper errors only surface at execution time, so a render assertion
// is the real gate here.
func TestSubscriptionsPage_Renders(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ls, err := logstore.New(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("logstore: %v", err)
	}
	t.Cleanup(func() { _ = ls.Close() })
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	h := &Handler{store: st, logStore: ls, renderer: r}

	resets := time.Now().Add(30 * time.Hour).UTC().Format(time.RFC3339)
	report := `{"source":"openrouter_key","points":[
		{"window":"weekly","scope":"api_key","used":9,"limit":10,"remaining":1,"unit":"usd","resets_at":"` + resets + `"},
		{"window":"key_limit","scope":"key","used":20,"limit":100,"remaining":80,"unit":"usd"}
	]}`
	ch := &model.Channel{
		Name: "or-main", Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1",
		Models: []string{"m1"}, Status: model.ChannelEnabled,
		UsageProbe: model.UsageProbe{Kind: model.UsageProbeOpenRouter},
	}
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.UpdateChannelUsageState(ch.ID, report, time.Now(), ""); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	// A probe-less channel: hidden by default, shown with filter=all.
	plain := &model.Channel{
		Name: "plain-ch", Provider: "openai", BaseURL: "https://x",
		Models: []string{"m1"}, Status: model.ChannelEnabled,
	}
	if err := st.CreateChannel(plain); err != nil {
		t.Fatalf("create plain: %v", err)
	}

	t.Run("default", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.SubscriptionsPage(rec, httptest.NewRequest(http.MethodGet, "/admin/subscriptions", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, want := range []string{
			"订阅用量", "or-main", "厂商权威", "Key 限额", "每周",
			"90%", // weekly is 9/10
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("missing %q in page", want)
			}
		}
		if strings.Contains(body, "plain-ch") {
			t.Fatal("a channel without a probe must be hidden by default")
		}
	})

	t.Run("show all", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.SubscriptionsPage(rec, httptest.NewRequest(http.MethodGet, "/admin/subscriptions?filter=all", nil))
		body := rec.Body.String()
		if !strings.Contains(body, "plain-ch") {
			t.Fatal("filter=all must list probe-less channels")
		}
		if !strings.Contains(body, "无数据") {
			t.Fatal("probe-less channel should show the 'no data' badge")
		}
	})
}

func TestSubscriptionsPage_EmptyState(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r, _ := NewRenderer()
	h := &Handler{store: st, renderer: r}
	rec := httptest.NewRecorder()
	h.SubscriptionsPage(rec, httptest.NewRequest(http.MethodGet, "/admin/subscriptions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "还没有配置用量探针的渠道") {
		t.Fatalf("empty state missing")
	}
}

func TestSubscriptionsPage_ExperimentalBadge(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r, _ := NewRenderer()
	h := &Handler{store: st, renderer: r}

	ch := &model.Channel{
		Name: "glm", Provider: "zhipu", BaseURL: "https://open.bigmodel.cn/api/paas/v4",
		Models: []string{"m1"}, Status: model.ChannelEnabled,
		UsageProbe: model.UsageProbe{Kind: model.UsageProbeZhipuWeb},
	}
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("create: %v", err)
	}
	report := `{"source":"zhipu_web","experimental":true,"warn":"dashboard shape may change",
		"points":[{"window":"rolling_5h","used":500,"limit":2000,"remaining":1500,"unit":"credits"}]}`
	if err := st.UpdateChannelUsageState(ch.ID, report, time.Now(), ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := httptest.NewRecorder()
	h.SubscriptionsPage(rec, httptest.NewRequest(http.MethodGet, "/admin/subscriptions", nil))
	body := rec.Body.String()
	for _, want := range []string{"实验性", "5 小时滚动", "dashboard shape may change"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q", want)
		}
	}
	// The credits unit rides along with the amounts (500 of 2000).
	if !strings.Contains(body, "500") || !strings.Contains(body, "2000") {
		t.Fatalf("credits amounts missing")
	}
}

func TestSubscriptionRefresh_KeepsGaugeAndLocalStrip(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r, _ := NewRenderer()
	// The local-estimate footer needs the P21 tracker wired in.
	h := &Handler{store: st, renderer: r, usageTrk: usage.New()}

	ch := &model.Channel{
		Name: "or-ch", Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1",
		Models: []string{"m1"}, Status: model.ChannelEnabled,
		UsageProbe: model.UsageProbe{Kind: model.UsageProbeOpenRouter},
		PlanQuota: model.PlanQuota{
			Metric: model.QuotaMetricTokens, Limit: 1000, Window: model.QuotaWindowDaily,
		},
	}
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("create: %v", err)
	}
	report := `{"source":"openrouter_key","points":[
		{"window":"weekly","used":9,"limit":10,"remaining":1,"unit":"usd"},
		{"window":"key_limit","used":20,"limit":100,"remaining":80,"unit":"usd"}
	]}`
	if err := st.UpdateChannelUsageState(ch.ID, report, time.Now(), ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/subscriptions/1/refresh", nil)
	stored, err := st.GetChannel(ch.ID)
	if err != nil {
		t.Fatalf("reload channel: %v", err)
	}
	h.renderSubscriptionCard(rec, req, stored)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The ring gauge must survive the HTMX swap — it is part of the
	// card's shape, so losing it makes the page jump.
	if !strings.Contains(body, "stroke-dashoffset") {
		t.Fatalf("ring gauge missing from the refreshed card")
	}
	// The local-estimate footer must survive too.
	if !strings.Contains(body, "本地估算") {
		t.Fatalf("local estimate strip missing from the refreshed card")
	}
	if !strings.Contains(body, "厂商权威") {
		t.Fatalf("vendor badge missing from the refreshed card")
	}
}

func TestHumanUntil(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		at   time.Time
		want string
	}{
		{now.Add(-time.Hour), "已重置"},
		{now.Add(12 * time.Minute), "12m"},
		{now.Add(2*time.Hour + 15*time.Minute), "2h 15m"},
		{now.Add(3*24*time.Hour + 4*time.Hour), "3d 4h"},
	}
	for _, c := range cases {
		if got := humanUntil(c.at, now); got != c.want {
			t.Fatalf("humanUntil(%v) = %q, want %q", c.at.Sub(now), got, c.want)
		}
	}
}

func TestWindowRankOrdering(t *testing.T) {
	// The most constraining windows must sort first.
	order := []string{
		subusage.WindowRolling, subusage.WindowKeyLimit, subusage.WindowDaily,
		subusage.WindowWeekly, subusage.WindowMonthly, subusage.WindowCredits,
	}
	for i := 1; i < len(order); i++ {
		if windowRank(order[i-1]) >= windowRank(order[i]) {
			t.Fatalf("window order broken at %d: %v", i, order)
		}
	}
	if windowRank("unknown_window") != 99 {
		t.Fatal("unknown windows must sort last")
	}
}
