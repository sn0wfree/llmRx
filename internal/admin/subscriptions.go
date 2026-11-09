package admin

import (
	"net/http"
	"sort"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/subusage"
)

// ---------- vendor subscription usage (P22) ----------
//
//	GET  /admin/api/v1/subscriptions               all probes + latest report
//	POST /admin/api/v1/subscriptions/{id}/refresh  probe one channel now
//	GET  /admin/api/v1/subscriptions/{id}/history  usage-point time series
//	GET  /admin/api/v1/subscription-parsers        registered parser names
//
// Everything here reads channels.usage_state (the cached report); only
// refresh talks to the vendor.

// SubscriptionView is one channel's subscription standing, as the
// vendor last reported it.
type SubscriptionView struct {
	ChannelID   int64  `json:"channel_id"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	ProbeKind   string `json:"probe_kind"`
	Experimental bool  `json:"experimental,omitempty"`

	Report *subusage.Report `json:"report,omitempty"`
	// Points is a flattened convenience view: one entry per window with
	// the computed ratio, so a UI need not redo the maths.
	Points []SubscriptionPoint `json:"points,omitempty"`

	// LocalQuota is the P21 locally-measured quota, shown next to the
	// vendor figure so drift is visible (a channel shared with other
	// tools consumes more than llmRx counted).
	LocalQuota *LocalQuotaView `json:"local_quota,omitempty"`

	CheckedAt *time.Time `json:"checked_at,omitempty"`
	Error     string     `json:"error,omitempty"`
	Warn      string     `json:"warn,omitempty"`
}

// SubscriptionPoint is one window with its computed ratio.
type SubscriptionPoint struct {
	Window    string     `json:"window"`
	Scope     string     `json:"scope,omitempty"`
	Label     string     `json:"label,omitempty"`
	Used      float64    `json:"used"`
	Limit     float64    `json:"limit,omitempty"`
	Remaining float64    `json:"remaining"`
	Unit      string     `json:"unit,omitempty"`
	Ratio     float64    `json:"ratio"`
	Exhausted bool       `json:"exhausted"`
	ResetsAt  *time.Time `json:"resets_at,omitempty"`
	Note      string     `json:"note,omitempty"`
}

// LocalQuotaView is the P21 view of the same channel.
type LocalQuotaView struct {
	Metric string  `json:"metric"`
	Used   float64 `json:"used"`
	Limit  float64 `json:"limit"`
	Ratio  float64 `json:"ratio"`
	Window string  `json:"window"`
}

// ListSubscriptions answers GET /subscriptions.
func (h *Handler) ListSubscriptions(w http.ResponseWriter, r *http.Request) {
	chs, err := h.store.GetChannels()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now()
	out := make([]SubscriptionView, 0, len(chs))
	for i := range chs {
		ch := &chs[i]
		if !ch.UsageProbe.Enabled() {
			continue
		}
		view := SubscriptionView{
			ChannelID:  ch.ID,
			Name:       ch.Name,
			Provider:   ch.Provider,
			ProbeKind:  ch.UsageProbe.Kind,
			CheckedAt:  ch.UsageCheckedAt,
			Error:      ch.UsageError,
		}
		if state := subusage.ReportFromState(ch.UsageState); state.Source != "" {
			view.Report = &state
			view.Experimental = state.Experimental
			view.Warn = state.Warn
			for _, p := range state.Points {
				sp := SubscriptionPoint{
					Window:    p.Window,
					Scope:     p.Scope,
					Label:     p.Label,
					Used:      p.Used,
					Limit:     p.Limit,
					Remaining: p.Remaining,
					Unit:      p.Unit,
					Ratio:     p.Ratio(),
					Exhausted: p.Exhausted(),
					Note:      p.Note,
				}
				if !p.ResetsAt.IsZero() {
					t := p.ResetsAt
					sp.ResetsAt = &t
				}
				view.Points = append(view.Points, sp)
			}
		}
		if ch.PlanQuota.Tracked() && h.usageTrk != nil {
			u := h.usageTrk.Usage(ch, now)
			view.LocalQuota = &LocalQuotaView{
				Metric: ch.PlanQuota.Metric,
				Used:   u.Used,
				Limit:  ch.PlanQuota.Limit,
				Ratio:  u.Ratio,
				Window: ch.PlanQuota.Window,
			}
		}
		out = append(out, view)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChannelID < out[j].ChannelID })
	writeJSON(w, http.StatusOK, map[string]any{
		"generated_at": now.UTC(),
		"data":         out,
	})
}

// RefreshSubscription answers POST /subscriptions/{id}/refresh.
func (h *Handler) RefreshSubscription(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if h.usageMon == nil {
		writeErr(w, http.StatusServiceUnavailable, "usage monitor not configured")
		return
	}
	ch, err := h.store.GetChannel(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	if !ch.UsageProbe.Enabled() {
		writeErr(w, http.StatusConflict, "channel has no usage probe configured")
		return
	}

	rep, qerr := h.usageMon.QueryChannel(r.Context(), ch)
	// Re-read so the response carries the persisted state even when the
	// probe failed (the monitor writes the error and keeps the old value).
	updated, rerr := h.store.GetChannel(id)
	if rerr != nil {
		updated = ch
	}

	resp := map[string]any{
		"channel_id": id,
		"name":       updated.Name,
		"checked_at": updated.UsageCheckedAt,
	}
	if qerr != nil {
		resp["error"] = qerr.Error()
		// The monitor persisted this already; 502 tells the caller the
		// upstream was at fault rather than the request.
		writeJSON(w, http.StatusBadGateway, resp)
		return
	}
	resp["source"] = rep.Source
	resp["points"] = rep.Points
	if rep.Warn != "" {
		resp["warn"] = rep.Warn
	}
	if rep.Experimental {
		resp["experimental"] = true
	}
	writeJSON(w, http.StatusOK, resp)
}

// SubscriptionHistory answers GET /subscriptions/{id}/history?days=N.
func (h *Handler) SubscriptionHistory(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	days := atoiOr(r.URL.Query().Get("days"), 30)
	if days <= 0 {
		days = 30
	}
	if days > 365 {
		days = 365
	}
	from := time.Now().UTC().AddDate(0, 0, -days)
	points, err := h.store.UsagePoints(id, from, 5000)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if points == nil {
		points = []store.UsagePoint{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channel_id": id,
		"days":       days,
		"data":       points,
	})
}

// ListSubscriptionParsers answers GET /subscription-parsers so the
// channel form can offer the registered probe kinds.
func (h *Handler) ListSubscriptionParsers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"data": subusage.ParserNames(),
		"notes": map[string]string{
			model.UsageProbeOpenRouter: "官方 API（GET /api/v1/key），支持日/周/月预算与 key 限额",
			model.UsageProbeZhipuWeb:   "实验性：Cookie 抓取 GLM Coding Plan 仪表盘，页面改版会失效",
			model.UsageProbeCustom:     "通用 JSONPath 提取，需填 endpoint_url + json_path + window_name",
		},
	})
}
