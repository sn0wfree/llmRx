package webui

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/subusage"
)

// windowOrder gives the UI a stable, meaningful ordering: the windows
// that block traffic first come first.
var windowOrder = map[string]int{
	subusage.WindowRolling:  0,
	subusage.WindowKeyLimit: 1,
	subusage.WindowDaily:    2,
	subusage.WindowWeekly:   3,
	subusage.WindowMonthly:  4,
	subusage.WindowCredits:  5,
}

func windowRank(name string) int {
	if r, ok := windowOrder[name]; ok {
		return r
	}
	return 99
}

// windowLabel is the human name shown next to a window.
func windowLabel(name string) string {
	switch name {
	case subusage.WindowRolling:
		return "5 小时滚动"
	case subusage.WindowKeyLimit:
		return "Key 限额"
	case subusage.WindowDaily:
		return "每日"
	case subusage.WindowWeekly:
		return "每周"
	case subusage.WindowMonthly:
		return "每月"
	case subusage.WindowCredits:
		return "余额"
	}
	return name
}

// SubscriptionsPage renders /admin/subscription-quota.
func (h *Handler) SubscriptionsPage(w http.ResponseWriter, r *http.Request) {
	chs, err := h.store.GetChannels()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// "probed" filter: default shows every channel that has a probe; the
	// "all" tab also lists channels without one so the operator can see
	// where the feature is unconfigured.
	showAll := r.URL.Query().Get("filter") == "all"
	now := time.Now()

	type pointView struct {
		Window    string
		Label     string
		Scope     string
		Used      float64
		Limit     float64
		Remaining float64
		Unit      string
		Ratio     float64
		Exhausted bool
		ResetsAt  *time.Time
		ResetIn   string
		Note      string
	}
	type channelView struct {
		ID           int64
		Name         string
		Provider     string
		ProbeKind    string
		Experimental bool
		Points       []pointView
		// Tightest is the window closest to exhaustion — the one that
		// actually constrains traffic. It drives the ring gauge in the
		// card header.
		Tightest *pointView
		LocalQuota *struct {
			Used  float64
			Limit float64
			Ratio float64
			Unit  string
		}
		CheckedAt *time.Time
		Error     string
		Warn      string
		Objective string // "vendor" | "local" | "none"
	}

	out := make([]channelView, 0, len(chs))
	exhaustedCount := 0
	for i := range chs {
		ch := &chs[i]
		if !ch.UsageProbe.Enabled() && !showAll {
			continue
		}
		view := channelView{
			ID:        ch.ID,
			Name:      ch.Name,
			Provider:  ch.Provider,
			ProbeKind: ch.UsageProbe.Kind,
			CheckedAt: ch.UsageCheckedAt,
			Error:     ch.UsageError,
			Objective: "none",
		}
		if rep := subusage.ReportFromState(ch.UsageState); rep.Source != "" && len(rep.Points) > 0 {
			view.Experimental = rep.Experimental
			view.Warn = rep.Warn
			view.Objective = "vendor"
			pts := make([]pointView, 0, len(rep.Points))
			for _, p := range rep.Points {
				pv := pointView{
					Window:    p.Window,
					Label:     p.Label,
					Scope:     p.Scope,
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
					pv.ResetsAt = &t
					pv.ResetIn = humanUntil(t, now)
				}
				if pv.Exhausted {
					exhaustedCount++
				}
				pts = append(pts, pv)
			}
			sort.SliceStable(pts, func(a, b int) bool {
				if windowRank(pts[a].Window) != windowRank(pts[b].Window) {
					return windowRank(pts[a].Window) < windowRank(pts[b].Window)
				}
				return pts[a].Label < pts[b].Label
			})
			view.Points = pts
			// The window closest to exhaustion drives the ring gauge.
			for i := range pts {
				if pts[i].Limit <= 0 {
					continue
				}
				if view.Tightest == nil || pts[i].Ratio > view.Tightest.Ratio {
					view.Tightest = &pts[i]
				}
			}
		}
		if ch.PlanQuota.Tracked() && h.usageTrk != nil {
			u := h.usageTrk.Usage(ch, now)
			unit := ch.PlanQuota.Metric
			view.LocalQuota = &struct {
				Used  float64
				Limit float64
				Ratio float64
				Unit  string
			}{Used: u.Used, Limit: u.Limit, Ratio: u.Ratio, Unit: unit}
			if view.Objective == "none" {
				view.Objective = "local"
			}
		}
		out = append(out, view)
	}

	data := map[string]any{
		"Body":           "subscriptions_body",
		"Title":          "订阅配额",
		"User":           userToView(getUser(r)),
		"Active":         "subscription-quota",
		"Channels":       out,
		"ShowAll":        showAll,
		"ExhaustedCount": exhaustedCount,
		"HasMonitor":     h.usageMon != nil,
		"WindowLabels":   windowLabels(),
	}
	if err := h.renderer.Render(w, "subscriptions_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// windowLabels exposes the label map to templates.
func windowLabels() map[string]string {
	out := map[string]string{}
	for _, w := range []string{
		subusage.WindowRolling, subusage.WindowKeyLimit, subusage.WindowDaily,
		subusage.WindowWeekly, subusage.WindowMonthly, subusage.WindowCredits,
	} {
		out[w] = windowLabel(w)
	}
	return out
}

// humanUntil renders a coarse countdown ("3d 4h", "2h 15m", "12m").
func humanUntil(t, now time.Time) string {
	d := t.Sub(now)
	if d <= 0 {
		return "已重置"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return strconv.Itoa(days) + "d " + strconv.Itoa(hours) + "h"
	case hours > 0:
		return strconv.Itoa(hours) + "h " + strconv.Itoa(mins) + "m"
	default:
		return strconv.Itoa(mins) + "m"
	}
}

// parsePathID reads a chi URL param as an int64.
func parsePathID(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, name), 10, 64)
}

// parseItoa is a tiny wrapper so the query parsing above stays readable.
func parseItoa(s string) (int, error) {
	n, err := strconv.Atoi(s)
	return n, err
}

// SubscriptionRefresh probes one channel now and re-renders its card.
func (h *Handler) SubscriptionRefresh(w http.ResponseWriter, r *http.Request) {
	id, err := parsePathID(r, "id")
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	ch, err := h.store.GetChannel(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if h.usageMon != nil && ch.UsageProbe.Enabled() {
		// The monitor persists the outcome (success or error); the card
		// below re-reads from the store, so a failed probe still renders
		// the last good report plus the error line.
		_, _ = h.usageMon.QueryChannel(r.Context(), ch)
	}
	updated, err := h.store.GetChannel(id)
	if err != nil {
		http.Error(w, "reload failed", http.StatusInternalServerError)
		return
	}
	h.renderSubscriptionCard(w, r, updated)
}

// renderSubscriptionCard renders one channel card as an HTMX outerHTML
// fragment.
func (h *Handler) renderSubscriptionCard(w http.ResponseWriter, r *http.Request, ch *model.Channel) {
	now := time.Now()
	type pointView struct {
		Window, Label, Scope         string
		Used, Limit, Remaining, Ratio float64
		Unit                         string
		Exhausted                    bool
		ResetsAt                     *time.Time
		ResetIn, Note                string
	}
	card := map[string]any{
		"Channel":    ch,
		"Name":       ch.Name,
		"ID":         ch.ID,
		"Provider":   ch.Provider,
		"ProbeKind":  ch.UsageProbe.Kind,
		"CheckedAt":  ch.UsageCheckedAt,
		"Error":      ch.UsageError,
		"HasMonitor": h.usageMon != nil,
		// Objective and LocalQuota drive the footer strip; without
		// them an HTMX refresh would drop it.
		"Objective": "none",
	}
	var points []pointView
	var tightest *pointView
	if rep := subusage.ReportFromState(ch.UsageState); rep.Source != "" {
		card["Experimental"] = rep.Experimental
		card["Warn"] = rep.Warn
		for _, p := range rep.Points {
			pv := pointView{
				Window: p.Window, Label: p.Label, Scope: p.Scope,
				Used: p.Used, Limit: p.Limit, Remaining: p.Remaining,
				Unit: p.Unit, Ratio: p.Ratio(), Exhausted: p.Exhausted(), Note: p.Note,
			}
			if !p.ResetsAt.IsZero() {
				t := p.ResetsAt
				pv.ResetsAt = &t
				pv.ResetIn = humanUntil(t, now)
			}
			points = append(points, pv)
		}
		sort.SliceStable(points, func(a, b int) bool {
			return windowRank(points[a].Window) < windowRank(points[b].Window)
		})
		// The ring gauge needs the tightest window, exactly like the
		// full page — without it an HTMX refresh would drop the gauge
		// and the card would visibly change shape.
		for i := range points {
			if points[i].Limit <= 0 {
				continue
			}
			if tightest == nil || points[i].Ratio > tightest.Ratio {
				tightest = &points[i]
			}
		}
	}
	card["Points"] = points
	card["Tightest"] = tightest
	if rep := subusage.ReportFromState(ch.UsageState); rep.Source != "" {
		card["Objective"] = "vendor"
	}
	if ch.PlanQuota.Tracked() && h.usageTrk != nil {
		u := h.usageTrk.Usage(ch, now)
		card["LocalQuota"] = map[string]any{
			"Used":  u.Used,
			"Limit": ch.PlanQuota.Limit,
			"Ratio": u.Ratio,
			"Unit":  ch.PlanQuota.Metric,
		}
		if card["Objective"] == "none" {
			card["Objective"] = "local"
		}
	}
	if err := h.renderer.RenderPartial(w, "subscription_card", card); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// SubscriptionHistoryPage returns one channel's burn-down series JSON
// for the inline chart.
func (h *Handler) SubscriptionHistoryPage(w http.ResponseWriter, r *http.Request) {
	chID, perr := parsePathID(r, "id")
	if perr != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, e := parseItoa(v); e == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	from := time.Now().UTC().AddDate(0, 0, -days)
	pts, err := h.store.UsagePoints(chID, from, 5000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Group by window so the chart can draw one line per window.
	byWindow := map[string][]store.UsagePoint{}
	for _, p := range pts {
		byWindow[p.Window] = append(byWindow[p.Window], p)
	}
	type series struct {
		Window string  `json:"window"`
		Label  string  `json:"label"`
		Unit   string  `json:"unit"`
		Points []struct {
			T     int64   `json:"t"`
			Used  float64 `json:"used"`
			Limit float64 `json:"limit"`
		} `json:"points"`
	}
	out := make([]series, 0, len(byWindow))
	for w, list := range byWindow {
		s := series{Window: w, Label: windowLabel(w)}
		for _, p := range list {
			s.Unit = p.Unit
			s.Points = append(s.Points, struct {
				T     int64   `json:"t"`
				Used  float64 `json:"used"`
				Limit float64 `json:"limit"`
			}{T: p.TS.Unix(), Used: p.Used, Limit: p.LimitVal})
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return windowRank(out[i].Window) < windowRank(out[j].Window) })
	writeJSONBody(w, map[string]any{"channel_id": chID, "days": days, "data": out})
}

func writeJSONBody(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
