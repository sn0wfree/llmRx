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
	"github.com/sn0wfree/llmRx/internal/usage"
)

// TestMonitoringPage_Renders exercises the full render path of the
// P21 monitoring page — template helper errors (mul100/fmtQuotaUsed/
// fmtWindow/min100) only surface at execution time, not parse time.
// The handler is driven directly (auth lives in the route
// middleware); the nil user renders the body without the sidebar,
// exactly like the login-page render path.
func TestMonitoringPage_Renders(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ls, err := logstore.New(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("logstore.New: %v", err)
	}
	t.Cleanup(func() { _ = ls.Close() })
	ls.SetAsyncConfig(logstore.AsyncConfig{Enabled: false})
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	trk := usage.New()
	h := &Handler{store: st, logStore: ls, renderer: r, usageTrk: trk}

	ch := &model.Channel{
		Name:     "code-ch",
		Provider: "openai",
		BaseURL:  "https://x",
		Models:   []string{"m1"},
		Status:   model.ChannelEnabled,
		PlanQuota: model.PlanQuota{
			Kind: "code", Metric: model.QuotaMetricTokens,
			Limit: 1000, Window: model.QuotaWindowRolling, WindowHours: 5,
		},
	}
	if err := h.store.CreateChannel(ch); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	trk.Observe(ch.ID, 800, 100, 50, 20, 0.5, time.Now())

	for _, rangeQ := range []string{"", "?range=7d", "?range=30d"} {
		req := httptest.NewRequest(http.MethodGet, "/admin/monitoring"+rangeQ, nil)
		rec := httptest.NewRecorder()
		h.MonitoringPage(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("monitoring%s: %d %s", rangeQ, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "用量监控") || !strings.Contains(body, "code-ch") {
			t.Fatalf("page missing expected content; full body:\n%s", body)
		}
		if rangeQ == "" {
			// Live quota gauge: used 900 of 1000 (rolling 5h window).
			if !strings.Contains(body, "900") || !strings.Contains(body, "1000") {
				t.Fatalf("quota gauge values missing")
			}
			if !strings.Contains(body, "code plan") || !strings.Contains(body, "滚动 5h") {
				t.Fatalf("quota labels missing")
			}
		}
	}
}
