package admin_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// seedUsageChannel creates a channel with a plan quota attached.
func seedUsageChannel(t *testing.T, app *testhelper.App, sess, name string, quota model.PlanQuota) model.Channel {
	t.Helper()
	body := fmt.Sprintf(`{
		"name":%q,"provider":"openai","base_url":"https://x","models":["m1"],
		"plan_quota":{"kind":%q,"metric":%q,"limit":%v,"window":%q,"window_hours":%d}
	}`, name, quota.Kind, quota.Metric, quota.Limit, quota.Window, quota.WindowHours)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("create channel: %d %s", rec.Code, rec.Body.String())
	}
	var ch model.Channel
	decodeJSON(t, rec, &ch)
	return ch
}

// seedUsageLog inserts one usage row straight into the logstore
// (async is disabled in testhelper, so Insert is synchronous).
func seedUsageLog(t *testing.T, app *testhelper.App, chID int64, prompt, completion, cached, reasoning int, realUSD float64, at time.Time) {
	t.Helper()
	if err := app.LogStore.Insert(&model.Log{
		ChannelID:        chID,
		Model:            "m1",
		PromptTokens:     prompt,
		CompletionTokens: completion,
		CachedTokens:     cached,
		ReasoningTokens:  reasoning,
		RealCostUSD:      realUSD,
		BilledCostUSD:    realUSD * 1.2,
		StatusCode:       200,
		CreatedAt:        at,
	}); err != nil {
		t.Fatalf("insert log: %v", err)
	}
}

func TestAdmin_UsageOverview(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	ch := seedUsageChannel(t, app, sess, "code-plan", model.PlanQuota{
		Kind: "code", Metric: model.QuotaMetricTokens, Limit: 1000, Window: model.QuotaWindowMonthly,
	})
	other := seedUsageChannel(t, app, sess, "payg", model.PlanQuota{})

	now := time.Now()
	seedUsageLog(t, app, ch.ID, 300, 200, 50, 40, 0.02, now.Add(-time.Hour))
	seedUsageLog(t, app, ch.ID, 100, 100, 0, 0, 0.01, now.Add(-2*time.Hour))
	seedUsageLog(t, app, other.ID, 10, 5, 0, 0, 0.001, now.Add(-time.Hour))
	seedUsageLog(t, app, ch.ID, 10, 0, 0, 0, 0, now.Add(-3*time.Hour)) // 500 row
	if err := app.LogStore.Insert(&model.Log{
		ChannelID: ch.ID, StatusCode: 500, CreatedAt: now.Add(-90 * time.Minute),
	}); err != nil {
		t.Fatalf("insert error log: %v", err)
	}
	// The live quota gauge reads the in-process tracker; fold the same
	// rows in (what the emitter would have done for real traffic).
	app.UsageTrk.Observe(ch.ID, 300, 200, 50, 40, 0.02, now.Add(-time.Hour))
	app.UsageTrk.Observe(ch.ID, 100, 100, 0, 0, 0.01, now.Add(-2*time.Hour))
	app.UsageTrk.Observe(other.ID, 10, 5, 0, 0, 0.001, now.Add(-time.Hour))
	app.UsageTrk.Observe(ch.ID, 10, 0, 0, 0, 0, now.Add(-3*time.Hour))

	rec := do(t, app.Admin.Routes(), http.MethodGet, "/usage/overview?range=24h", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("overview: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Range  string `json:"range"`
		Totals struct {
			Requests         int64   `json:"requests"`
			PromptTokens     int64   `json:"prompt_tokens"`
			CompletionTokens int64   `json:"completion_tokens"`
			CachedTokens     int64   `json:"cached_tokens"`
			ReasoningTokens  int64   `json:"reasoning_tokens"`
			Errors           int64   `json:"errors"`
			ErrorRate        float64 `json:"error_rate"`
		} `json:"totals"`
		Data []struct {
			ID               int64          `json:"id"`
			Name             string         `json:"name"`
			PlanKind         string         `json:"plan_kind"`
			Requests         int64          `json:"requests"`
			PromptTokens     int64          `json:"prompt_tokens"`
			CompletionTokens int64          `json:"completion_tokens"`
			CachedTokens     int64          `json:"cached_tokens"`
			ReasoningTokens  int64          `json:"reasoning_tokens"`
			RealCostUSD      float64        `json:"real_cost_usd"`
			ErrorRate        float64        `json:"error_rate"`
			Quota            *struct {
				Limit float64 `json:"limit"`
				Used  float64 `json:"used"`
				Ratio float64 `json:"ratio"`
			} `json:"quota"`
		} `json:"data"`
	}
	decodeJSON(t, rec, &resp)
	if resp.Range != "24h" {
		t.Fatalf("range = %q", resp.Range)
	}
	// 5 rows total (4 seeded + 1 error row); 1 of them a 500.
	if resp.Totals.Requests != 5 || resp.Totals.Errors != 1 {
		t.Fatalf("totals: %+v", resp.Totals)
	}
	if resp.Totals.PromptTokens != 300+100+10+10 {
		t.Fatalf("prompt tokens: %+v", resp.Totals)
	}
	if resp.Totals.CachedTokens != 50 || resp.Totals.ReasoningTokens != 40 {
		t.Fatalf("split tokens: %+v", resp.Totals)
	}

	byName := map[string]int{}
	for i, row := range resp.Data {
		byName[row.Name] = i
	}
	i := byName["code-plan"]
	if resp.Data[i].PlanKind != "code" || resp.Data[i].Requests != 4 {
		t.Fatalf("code-plan row: %+v", resp.Data[i])
	}
	if resp.Data[i].Quota == nil {
		t.Fatalf("tracked channel must carry a quota gauge")
	}
	if resp.Data[i].Quota.Used != 710 {
		t.Fatalf("quota used = %v, want 710 (prompt+completion incl. the 500 row)", resp.Data[i].Quota.Used)
	}
	if resp.Data[i].Quota.Ratio <= 0.7 || resp.Data[i].Quota.Ratio >= 0.8 {
		t.Fatalf("quota ratio = %v", resp.Data[i].Quota.Ratio)
	}
	j := byName["payg"]
	if resp.Data[j].Quota != nil {
		t.Fatalf("untracked channel must not carry a quota gauge")
	}
}

func TestAdmin_UsageOverviewRangeValidation(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	for _, q := range []string{"range=7d", "range=30d", ""} {
		rec := do(t, app.Admin.Routes(), http.MethodGet, "/usage/overview?"+q, sess, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("range %q: %d %s", q, rec.Code, rec.Body.String())
		}
	}
}

func TestAdmin_UsageTimeseries(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	ch := seedUsageChannel(t, app, sess, "ts-ch", model.PlanQuota{})
	now := time.Now()
	seedUsageLog(t, app, ch.ID, 100, 50, 0, 0, 0.01, now.Add(-time.Hour))
	seedUsageLog(t, app, ch.ID, 60, 40, 0, 0, 0.01, now.Add(-2*time.Hour))

	rec := do(t, app.Admin.Routes(), http.MethodGet, "/usage/timeseries?range=24h", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("timeseries: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Hourly bool `json:"hourly"`
		Data   []struct {
			ChannelID int64 `json:"channel_id"`
			Points    []struct {
				Bucket string  `json:"bucket"`
				Tokens int64   `json:"tokens"`
				Cost   float64 `json:"cost"`
			} `json:"points"`
		} `json:"data"`
	}
	decodeJSON(t, rec, &resp)
	if !resp.Hourly {
		t.Fatal("24h range must be hourly")
	}
	if len(resp.Data) != 1 || len(resp.Data[0].Points) < 2 {
		t.Fatalf("series: %+v", resp)
	}
	var total int64
	for _, p := range resp.Data[0].Points {
		total += p.Tokens
	}
	if total != 250 {
		t.Fatalf("series total tokens = %d, want 250", total)
	}
}

func TestAdmin_UsagePlans(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	rec := do(t, app.Admin.Routes(), http.MethodPost, "/plans", sess,
		`{"name":"gold","budget_usd":100,"markup_ratio":1.1,"status":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create plan: %d %s", rec.Code, rec.Body.String())
	}
	var plan model.Plan
	decodeJSON(t, rec, &plan)

	// A token bound to the plan.
	rec = do(t, app.Admin.Routes(), http.MethodPost, "/tokens", sess,
		`{"name":"bound","plan_id":`+fmt.Sprintf("%d", plan.ID)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create token: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, app.Admin.Routes(), http.MethodGet, "/usage/plans", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("usage plans: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []struct {
			Name       string  `json:"name"`
			BudgetUSD  float64 `json:"budget_usd"`
			UsedUSD    float64 `json:"used_usd"`
			UsageRatio float64 `json:"usage_ratio"`
			Tokens     int     `json:"tokens"`
		} `json:"data"`
	}
	decodeJSON(t, rec, &resp)
	if len(resp.Data) != 1 {
		t.Fatalf("plans rows = %d, want 1", len(resp.Data))
	}
	if resp.Data[0].Name != "gold" || resp.Data[0].UsageRatio != 0 || resp.Data[0].Tokens != 1 {
		t.Fatalf("plan row: %+v", resp.Data[0])
	}
}

// TestAdmin_ChannelPlanQuotaCRUD: the quota round-trips through the
// channels API and rejects invalid configs.
func TestAdmin_ChannelPlanQuotaCRUD(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	ch := seedUsageChannel(t, app, sess, "quota-ch", model.PlanQuota{
		Kind: "code", Metric: model.QuotaMetricTokens, Limit: 500, Window: model.QuotaWindowRolling, WindowHours: 5,
	})
	if ch.PlanQuota.Limit != 500 || ch.PlanQuota.WindowHours != 5 {
		t.Fatalf("created quota: %+v", ch.PlanQuota)
	}

	// Update: shrink the limit.
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/channels/"+fmt.Sprintf("%d", ch.ID), sess,
		`{"plan_quota":{"kind":"token","metric":"usd","limit":9.5,"window":"monthly"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update quota: %d %s", rec.Code, rec.Body.String())
	}
	var updated model.Channel
	decodeJSON(t, rec, &updated)
	if updated.PlanQuota.Kind != "token" || updated.PlanQuota.Limit != 9.5 || updated.PlanQuota.Window != "monthly" {
		t.Fatalf("updated quota: %+v", updated.PlanQuota)
	}

	// Clear by sending an empty quota. Fresh decode target: JSON
	// unmarshal merges into the old struct otherwise.
	rec = do(t, app.Admin.Routes(), http.MethodPut, "/channels/"+fmt.Sprintf("%d", ch.ID), sess, `{"plan_quota":{}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear quota: %d %s", rec.Code, rec.Body.String())
	}
	var cleared model.Channel
	decodeJSON(t, rec, &cleared)
	if cleared.PlanQuota.Tracked() {
		t.Fatalf("quota must be cleared: %+v", cleared.PlanQuota)
	}

	// Invalid metric is rejected.
	rec = do(t, app.Admin.Routes(), http.MethodPut, "/channels/"+fmt.Sprintf("%d", ch.ID), sess,
		`{"plan_quota":{"metric":"requests","limit":1,"window":"monthly"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid quota must 400, got %d %s", rec.Code, rec.Body.String())
	}
}
