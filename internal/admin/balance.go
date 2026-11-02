package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

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

// BalanceHistory returns one channel's balance time series (P14 phase 3).
//
// Query: ?days=N (default 30). Points are oldest-first and carry the
// channel's currency once, not per point: a channel's balance currency
// does not change over time.
func (h *Handler) BalanceHistory(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	ch, err := h.store.GetChannel(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "channel not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	days := atoiOr(r.URL.Query().Get("days"), 30)
	if days <= 0 || days > 365 {
		days = 30
	}
	from := time.Now().UTC().AddDate(0, 0, -days)

	// 5000 is a defensive ceiling, not an expected volume: an hourly
	// sweep over 90 days is ~2160 points.
	points, err := h.store.BalanceHistory(id, from, 5000)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if points == nil {
		points = []store.BalancePoint{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"channel_id": id,
		"name":       ch.Name,
		"currency":   ch.BalanceCurrency,
		"days":       days,
		"points":     points,
	})
}

// ChannelKeyBalances returns the per-key balance breakdown for one
// channel (P14 phase 4). Balance is account-level, so a multi-key
// channel has several.
func (h *Handler) ChannelKeyBalances(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if _, err := h.store.GetChannel(id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "channel not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	keys, err := h.store.GetKeys(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	type keyView struct {
		KeyID     int64  `json:"key_id"`
		Masked    string `json:"masked"`
		Status    int    `json:"status"`
		Balance   string `json:"balance,omitempty"`
		Currency  string `json:"currency,omitempty"`
		UpdatedAt string `json:"updated_at,omitempty"`
		Error     string `json:"error,omitempty"`
	}
	out := make([]keyView, 0, len(keys))
	for i := range keys {
		k := &keys[i]
		v := keyView{
			KeyID:    k.ID,
			Masked:   k.KeyMasked,
			Status:   int(k.Status),
			Balance:  k.Balance,
			Currency: k.BalanceCurrency,
			Error:    k.BalanceError,
		}
		if k.BalanceUpdatedAt != nil {
			v.UpdatedAt = k.BalanceUpdatedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channel_id": id,
		"keys":       out,
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
