package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/balance"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
)

// providerSupportsBalance reports whether a provider declares a balance
// endpoint (P14). Used by the channels list to render "—" instead of an
// empty cell for providers that cannot be queried at all.
func providerSupportsBalance(name string) bool {
	d, ok := provider.LookupProvider(name)
	return ok && d.Balance.Format != ""
}

// SetBalanceMonitor injects the balance monitor so the channels page can
// refresh a single row on demand.
func (h *Handler) SetBalanceMonitor(m BalanceQuerier) { h.balanceMon = m }

// BalanceQuerier is the narrow surface the row-refresh button needs.
type BalanceQuerier interface {
	QueryChannel(ctx context.Context, ch *model.Channel) (provider.Balance, error)
}

// ChannelBalanceRefresh re-queries one channel's upstream balance and
// re-renders just that table row (HTMX outerHTML swap).
//
// A failure is not an HTTP error: the error is persisted on the channel
// and rendered inside the row, so the operator sees it in place instead
// of losing the page to an error status.
func (h *Handler) ChannelBalanceRefresh(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if h.balanceMon == nil {
		http.Error(w, "balance monitor not configured", http.StatusServiceUnavailable)
		return
	}
	ch, err := h.store.GetChannel(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Ignore the error value: QueryChannel already wrote the outcome
	// (value or error) onto the channel row, which is what the
	// re-rendered cell reads.
	_, _ = h.balanceMon.QueryChannel(r.Context(), ch)

	// Re-read so the row reflects what was persisted, not what the
	// in-memory object happens to hold.
	if fresh, ferr := h.store.GetChannel(id); ferr == nil {
		ch = fresh
	}

	if err := h.renderer.RenderPartial(w, "channels_table_body", map[string]any{
		"Channels": []model.Channel{*ch},
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// compile-time assertion that the monitor satisfies the webui contract.
var _ BalanceQuerier = (*balance.Monitor)(nil)

// ChannelBalanceTrend renders (or collapses) the inline balance-trend row.
//
// Collapsing returns an empty string so the HTMX outerHTML swap removes
// the row entirely — that is simpler than tracking a visible flag.
func (h *Handler) ChannelBalanceTrend(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("collapse") != "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		return // empty body removes the row
	}

	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	ch, err := h.store.GetChannel(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	const days = 30
	from := time.Now().UTC().AddDate(0, 0, -days)
	points, err := h.store.BalanceHistory(id, from, 5000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// The chart wants plain numbers; the string form stays authoritative
	// in storage, this is only for plotting.
	type plotPoint struct {
		T int64   `json:"t"`
		V float64 `json:"v"`
	}
	series := make([]plotPoint, 0, len(points))
	for _, p := range points {
		v, perr := strconv.ParseFloat(p.Balance, 64)
		if perr != nil {
			continue // an unparsable point cannot be plotted
		}
		series = append(series, plotPoint{T: p.TS.Unix(), V: v})
	}
	raw, _ := json.Marshal(series)

	data := map[string]any{
		"ChannelID":  id,
		"Name":       ch.Name,
		"Currency":   ch.BalanceCurrency,
		"Points":     len(series),
		"HasData":    len(series) > 0,
		"Days":       days,
		"SeriesJSON": template.JS(raw),
	}
	if n := len(series); n >= 2 {
		delta := series[n-1].V - series[0].V
		data["ChangeText"] = fmt.Sprintf("%+.2f", delta)
		if delta < 0 {
			data["ChangeClass"] = "text-red-600"
		} else {
			data["ChangeClass"] = "text-green-600"
		}
	}

	if err := h.renderer.RenderPartial(w, "channel_balance_trend", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

