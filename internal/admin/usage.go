package admin

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/usage"
)

// ---------- usage monitoring (P21) ----------
//
// Three read-only endpoints feed the /admin/channel-monitor page:
//
//	GET /admin/api/v1/usage/overview?range=24h|7d|30d
//	GET /admin/api/v1/usage/timeseries?range=24h|7d|30d
//	GET /admin/api/v1/usage/plans
//
// Aggregation lives in internal/usage (meta rollups when the JSONL
// backend is active, ScanSince fallback otherwise). Live quota
// gauges come from the shared usage.Tracker (nil-safe: they are
// simply omitted).

// ChannelUsageRow is one channel's row in the overview response.
type ChannelUsageRow struct {
	ID               int64      `json:"id"`
	Name             string     `json:"name"`
	Provider         string     `json:"provider"`
	Status           int        `json:"status"`
	PlanKind         string     `json:"plan_kind,omitempty"`
	Quota            *QuotaView `json:"quota,omitempty"`
	Requests         int64      `json:"requests"`
	PromptTokens     int64      `json:"prompt_tokens"`
	CompletionTokens int64      `json:"completion_tokens"`
	CachedTokens     int64      `json:"cached_tokens"`
	ReasoningTokens  int64      `json:"reasoning_tokens"`
	RealCostUSD      float64    `json:"real_cost_usd"`
	BilledCostUSD    float64    `json:"billed_cost_usd"`
	ErrorRate        float64    `json:"error_rate"`
	Balance          string     `json:"balance,omitempty"`
	BalanceCurrency  string     `json:"balance_currency,omitempty"`
}

// QuotaView is the live plan-quota gauge for one channel.
type QuotaView struct {
	Kind        string    `json:"kind"`
	Metric      string    `json:"metric"`
	Limit       float64   `json:"limit"`
	Window      string    `json:"window"`
	WindowHours int       `json:"window_hours,omitempty"`
	Used        float64   `json:"used"`
	Ratio       float64   `json:"ratio"`
	WindowStart time.Time `json:"window_start"`
	ResetAt     time.Time `json:"reset_at"`
}

// quotaViewFrom converts the tracker's WindowUsage into the wire shape.
func quotaViewFrom(ch *model.Channel, u usage.WindowUsage) *QuotaView {
	q := ch.PlanQuota
	if !q.Tracked() {
		return nil
	}
	return &QuotaView{
		Kind:        q.Kind,
		Metric:      q.Metric,
		Limit:       q.Limit,
		Window:      q.Window,
		WindowHours: q.WindowHours,
		Used:        u.Used,
		Ratio:       u.Ratio,
		WindowStart: u.WindowStart,
		ResetAt:     u.ResetAt,
	}
}

// UsageOverview answers GET /usage/overview: per-channel token usage
// over the range plus live quota gauges and range totals.
func (h *Handler) UsageOverview(w http.ResponseWriter, r *http.Request) {
	chs, err := h.store.GetChannels()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now()
	from, rangeName, dayMode := usage.ParseRange(r.URL.Query().Get("range"), now)

	aggs, err := usage.AggregateChannelUsage(h.logStore, from, dayMode)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	rows := make([]ChannelUsageRow, 0, len(chs))
	var totals usage.ChannelAgg
	for i := range chs {
		ch := &chs[i]
		agg := aggs[ch.ID]
		if agg == nil {
			agg = &usage.ChannelAgg{}
		}
		totals.Add(agg)
		row := ChannelUsageRow{
			ID:               ch.ID,
			Name:             ch.Name,
			Provider:         ch.Provider,
			Status:           int(ch.Status),
			PlanKind:         ch.PlanQuota.Kind,
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
		if h.usageTrk != nil {
			row.Quota = quotaViewFrom(ch, h.usageTrk.Usage(ch, now))
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].BilledCostUSD != rows[j].BilledCostUSD {
			return rows[i].BilledCostUSD > rows[j].BilledCostUSD
		}
		return rows[i].ID < rows[j].ID
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"range":        rangeName,
		"generated_at": now.UTC(),
		"totals": map[string]any{
			"requests":          totals.Requests,
			"prompt_tokens":     totals.PromptTokens,
			"completion_tokens": totals.CompletionTokens,
			"cached_tokens":     totals.CachedTokens,
			"reasoning_tokens":  totals.ReasoningTokens,
			"real_cost_usd":     totals.RealCostUSD,
			"billed_cost_usd":   totals.BilledCostUSD,
			"errors":            totals.Errors,
			"error_rate":        totals.ErrorRate(),
		},
		"data": rows,
	})
}

// ChannelSeries is one channel's per-bucket token series for the
// timeseries response.
type ChannelSeries struct {
	ChannelID int64         `json:"channel_id"`
	Name      string        `json:"name,omitempty"`
	Points    []SeriesPoint `json:"points"`
}

// SeriesPoint is one bucket of a channel series.
type SeriesPoint struct {
	Bucket   string  `json:"bucket"` // "2006-01-02T15Z" (hourly) or "2006-01-02" (daily)
	Tokens   int64   `json:"tokens"`
	Prompt   int64   `json:"prompt"`
	Requests int64   `json:"requests"`
	Cost     float64 `json:"cost"`
}

// UsageTimeseries answers GET /usage/timeseries: per-channel token
// series for the trend chart. 24h buckets by hour, 7d/30d by day.
func (h *Handler) UsageTimeseries(w http.ResponseWriter, r *http.Request) {
	chs, err := h.store.GetChannels()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now()
	from, rangeName, _ := usage.ParseRange(r.URL.Query().Get("range"), now)
	hourly := rangeName == "24h"

	points, err := usage.ChannelBuckets(h.logStore, from, hourly)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	byID := make(map[int64]string, len(chs))
	for i := range chs {
		byID[chs[i].ID] = chs[i].Name
	}
	// Merge duplicate (bucket, channel) cells (same-day rollover seq
	// files each contribute a meta group).
	type cell struct {
		tokens, prompt, requests int64
		cost                     float64
	}
	merged := map[string]*cell{}
	order := []string{}
	for _, p := range points {
		key := p.Bucket + "|" + strconv.FormatInt(p.ChannelID, 10)
		c := merged[key]
		if c == nil {
			c = &cell{}
			merged[key] = c
			order = append(order, key)
		}
		c.tokens += p.Prompt + p.Completion
		c.prompt += p.Prompt
		c.requests += p.Requests
		c.cost += p.Cost
	}
	series := map[int64]*ChannelSeries{}
	for _, key := range order {
		c := merged[key]
		pipe := strings.IndexByte(key, '|')
		chID, _ := strconv.ParseInt(key[pipe+1:], 10, 64)
		s := series[chID]
		if s == nil {
			s = &ChannelSeries{ChannelID: chID, Name: byID[chID]}
			series[chID] = s
		}
		s.Points = append(s.Points, SeriesPoint{
			Bucket:   key[:pipe],
			Tokens:   c.tokens,
			Prompt:   c.prompt,
			Requests: c.requests,
			Cost:     c.cost,
		})
	}
	out := make([]*ChannelSeries, 0, len(series))
	for _, s := range series {
		sort.Slice(s.Points, func(i, j int) bool { return s.Points[i].Bucket < s.Points[j].Bucket })
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChannelID < out[j].ChannelID })

	writeJSON(w, http.StatusOK, map[string]any{
		"range":  rangeName,
		"hourly": hourly,
		"data":   out,
	})
}

// PlanUsageRow is one llmRx Plan's budget progress card.
type PlanUsageRow struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	BudgetUSD   float64 `json:"budget_usd"`
	UsedUSD     float64 `json:"used_usd"`
	UsageRatio  float64 `json:"usage_ratio"`
	MarkupRatio float64 `json:"markup_ratio"`
	Status      int     `json:"status"`
	Tokens      int     `json:"tokens"` // downstream tokens bound to this plan
}

// UsagePlans answers GET /usage/plans: downstream budget progress.
// The llmRx Plan ledger is lifetime (no reset window); the row shows
// used/budget and how many tokens are bound to the plan.
func (h *Handler) UsagePlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.store.GetPlans()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	tokens, terr := h.store.GetTokens()
	if terr != nil {
		writeErr(w, http.StatusInternalServerError, terr.Error())
		return
	}
	bound := map[int64]int{}
	for _, t := range tokens {
		if t.PlanID > 0 {
			bound[t.PlanID]++
		}
	}
	rows := make([]PlanUsageRow, 0, len(plans))
	for _, p := range plans {
		row := PlanUsageRow{
			ID:          p.ID,
			Name:        p.Name,
			BudgetUSD:   p.BudgetUSD,
			UsedUSD:     p.UsedUSD,
			MarkupRatio: p.MarkupRatio,
			Status:      p.Status,
			Tokens:      bound[p.ID],
		}
		if p.BudgetUSD > 0 {
			row.UsageRatio = p.UsedUSD / p.BudgetUSD
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": rows})
}
