package webui

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/usage"
)

// MonitoringPage renders the P21 usage monitoring view: per-channel
// token usage + plan-quota gauges for the selected range, the
// per-channel token trend chart, and the downstream Plan budget
// cards. Data is computed server-side (same aggregation as the
// /admin/api/v1/usage/* endpoints).
func (h *Handler) MonitoringPage(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	from, rangeName, dayMode := usage.ParseRange(r.URL.Query().Get("range"), now)

	chs, err := h.store.GetChannels()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	aggs, err := usage.AggregateChannelUsage(h.logStore, from, dayMode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var totals usage.ChannelAgg
	type chRow struct {
		ID               int64
		Name             string
		Provider         string
		Status           int
		Enabled          bool
		PlanKind         string
		Tracked          bool
		Quota            *usage.WindowUsage
		QuotaCfg         model.PlanQuota // metric/window labels for the gauge
		Requests         int64
		PromptTokens     int64
		CompletionTokens int64
		CachedTokens     int64
		ReasoningTokens  int64
		RealCostUSD      float64
		BilledCostUSD    float64
		ErrorRate        float64
		Balance          string
		BalanceCurrency  string
	}
	rows := make([]chRow, 0, len(chs))
	for i := range chs {
		ch := chs[i]
		agg := aggs[ch.ID]
		if agg == nil {
			agg = &usage.ChannelAgg{}
		}
		totals.Add(agg)
		row := chRow{
			ID:               ch.ID,
			Name:             ch.Name,
			Provider:         ch.Provider,
			Status:           int(ch.Status),
			Enabled:          ch.Status == model.ChannelEnabled,
			PlanKind:         ch.PlanQuota.Kind,
			Tracked:          ch.PlanQuota.Tracked(),
			QuotaCfg:         ch.PlanQuota,
			Requests:         agg.Requests,
			PromptTokens:     agg.PromptTokens,
			CompletionTokens: agg.CompletionTokens,
			CachedTokens:     agg.CachedTokens,
			ReasoningTokens:  agg.ReasoningTokens,
			RealCostUSD:      agg.RealCostUSD,
			BilledCostUSD:    agg.BilledCostUSD,
			ErrorRate:        agg.ErrorRate(),
			Balance:          ch.Balance,
			BalanceCurrency:  ch.BalanceCurrency,
		}
		if h.usageTrk != nil && ch.PlanQuota.Tracked() {
			u := h.usageTrk.Usage(&ch, now)
			row.Quota = &u
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].BilledCostUSD != rows[j].BilledCostUSD {
			return rows[i].BilledCostUSD > rows[j].BilledCostUSD
		}
		return rows[i].ID < rows[j].ID
	})

	// Trend series: (bucket, channel) cells → per-channel points JSON
	// consumed by the inline canvas chart.
	hourly := rangeName == "24h"
	points, err := usage.ChannelBuckets(h.logStore, from, hourly)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type chartPoint struct {
		Bucket string  `json:"bucket"`
		Tokens int64   `json:"tokens"`
		Cost   float64 `json:"cost"`
	}
	type chartSeries struct {
		ChannelID int64        `json:"channel_id"`
		Name      string       `json:"name"`
		Points    []chartPoint `json:"points"`
	}
	nameOf := make(map[int64]string, len(chs))
	for i := range chs {
		nameOf[chs[i].ID] = chs[i].Name
	}
	series := map[int64]*chartSeries{}
	for _, p := range points {
		s := series[p.ChannelID]
		if s == nil {
			s = &chartSeries{ChannelID: p.ChannelID, Name: nameOf[p.ChannelID]}
			series[p.ChannelID] = s
		}
		s.Points = append(s.Points, chartPoint{Bucket: p.Bucket, Tokens: p.Prompt + p.Completion, Cost: p.Cost})
	}
	out := make([]*chartSeries, 0, len(series))
	for _, s := range series {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChannelID < out[j].ChannelID })
	seriesJSON := "[]"
	if b, err := json.Marshal(out); err == nil {
		seriesJSON = string(b)
	}

	// Downstream Plan budget cards.
	plans, _ := h.store.GetPlans()
	tokens, _ := h.store.GetTokens()
	bound := map[int64]int{}
	for _, t := range tokens {
		if t.PlanID > 0 {
			bound[t.PlanID]++
		}
	}
	type planRow struct {
		ID         int64
		Name       string
		BudgetUSD  float64
		UsedUSD    float64
		UsageRatio float64
		Tokens     int
	}
	planRows := make([]planRow, 0, len(plans))
	for _, p := range plans {
		pr := planRow{ID: p.ID, Name: p.Name, BudgetUSD: p.BudgetUSD, UsedUSD: p.UsedUSD, Tokens: bound[p.ID]}
		if p.BudgetUSD > 0 {
			pr.UsageRatio = p.UsedUSD / p.BudgetUSD
		}
		planRows = append(planRows, pr)
	}

	data := map[string]any{
		"Body":           "monitoring_body",
		"Title":          "渠道监控",
		"User":           userToView(getUser(r)),
		"Active":         "channel-monitor",
		"Range":          rangeName,
		"Rows":           rows,
		"SeriesJSON":     seriesJSON,
		"Hourly":         hourly,
		"Plans":          planRows,
		"Totals":         &totals, // pointer so the template can call the ErrorRate method
		"QuotaGuardNote": "配额耗尽的渠道会被路由自动避开（L2.5 quota guard）",
	}
	if err := h.renderer.Render(w, "monitoring_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
