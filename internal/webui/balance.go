package webui

import (
	"context"
	"net/http"
	"strconv"

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

