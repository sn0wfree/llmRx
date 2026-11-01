package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/sn0wfree/llmRx/internal/balance"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/store"
)

// BalanceRefresher is the narrow surface the admin endpoints need.
// Declared here (not imported as a concrete type) so internal/admin
// does not depend on internal/balance and cannot drag its upstream
// HTTP path into unrelated tests.
type BalanceRefresher interface {
	QueryChannel(ctx context.Context, ch *model.Channel) (provider.Balance, error)
}

// balanceView is one channel's balance row as the UI sees it.
type balanceView struct {
	ChannelID   int64  `json:"channel_id"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Balance     string `json:"balance,omitempty"`
	Currency    string `json:"currency,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	Error       string `json:"error,omitempty"`
	// Supported is false when the provider declares no balance
	// endpoint; the UI renders "—" instead of an empty value.
	Supported bool `json:"supported"`
}

// ListBalances returns the persisted balance snapshot for every
// channel. Pure DB read: no upstream calls, safe to poll.
func (h *Handler) ListBalances(w http.ResponseWriter, r *http.Request) {
	channels, err := h.store.GetChannels()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]balanceView, 0, len(channels))
	for i := range channels {
		ch := &channels[i]
		v := balanceView{
			ChannelID: ch.ID,
			Name:      ch.Name,
			Provider:  ch.Provider,
			Balance:   ch.Balance,
			Currency:  ch.BalanceCurrency,
			Error:     ch.BalanceError,
			Supported: balanceSupported(ch),
		}
		if ch.BalanceUpdatedAt != nil {
			v.UpdatedAt = ch.BalanceUpdatedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		out = append(out, v)
	}

	// Per-currency totals. Never summed across currencies: 110 CNY and
	// 110 USD are not the same money.
	totals := map[string]float64{}
	for _, ch := range channels {
		if ch.Balance == "" {
			continue
		}
		v, err := strconv.ParseFloat(ch.Balance, 64)
		if err != nil {
			continue
		}
		cur := ch.BalanceCurrency
		if cur == "" {
			cur = "UNKNOWN"
		}
		totals[cur] += v
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"balances": out,
		"totals":   totals,
	})
}

// RefreshChannelBalance queries one upstream now and returns the
// refreshed row. Distinct status codes matter here:
//
//	503  no monitor wired (partial deployment)
//	404  channel does not exist
//	409  provider has no balance endpoint
//	502  upstream query failed (error is recorded on the channel)
func (h *Handler) RefreshChannelBalance(w http.ResponseWriter, r *http.Request) {
	if h.balanceMon == nil {
		writeErr(w, http.StatusServiceUnavailable, "balance monitor not configured")
		return
	}
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	ch, err := h.store.GetChannel(id)
	if err != nil {
		// Same not-found contract as the other channel endpoints.
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "channel not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	b, err := h.balanceMon.QueryChannel(r.Context(), ch)
	if err != nil {
		if errors.Is(err, balance.ErrUnsupported) {
			writeErr(w, http.StatusConflict, "provider does not support balance queries")
			return
		}
		// The error is already persisted on the channel row, so the
		// client can simply re-read /balances to see it.
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	// Re-read so the response reflects what was persisted, not whatever
	// the in-memory object happens to hold (monitors may batch or
	// normalise values before storing them).
	if fresh, ferr := h.store.GetChannel(id); ferr == nil {
		ch = fresh
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"channel_id": ch.ID,
		"name":       ch.Name,
		"balance":    ch.Balance,
		"currency":   ch.BalanceCurrency,
		"source":     b.Source,
		"note":       b.Note,
	})
}

// balanceSupported reports whether a channel's provider declares a
// balance endpoint.
func balanceSupported(ch *model.Channel) bool {
	d, ok := provider.LookupProvider(ch.Provider)
	if !ok {
		return false
	}
	return d.Balance.Format != ""
}
