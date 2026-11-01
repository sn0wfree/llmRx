// Package balance periodically refreshes the upstream account balance
// for every enabled channel and persists the snapshot on the channel
// row (P14).
//
// Unlike internal/prober — which keeps results in memory because health
// flaps every 30s — balances are slow-moving and operators want history
// across restarts, so the snapshot lives in the channels table. That
// also makes the read path (admin pages, API) a plain SELECT with no
// extra cache to keep coherent.
package balance

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
)

// Config controls the refresh loop.
type Config struct {
	// Interval between automatic sweeps. 0 disables the loop entirely
	// (manual refresh via the admin API still works).
	Interval time.Duration
	// Timeout bounds a single upstream query.
	Timeout time.Duration
	// Jitter is the fraction of Interval by which each sweep start is
	// randomised, so replicas do not hit upstreams in lockstep.
	Jitter float64
}

// DefaultConfig is the built-in configuration.
var DefaultConfig = Config{
	Interval: time.Hour,
	Timeout:  10 * time.Second,
	Jitter:   0.1,
}

// ChannelLister supplies the channels to consider.
type ChannelLister interface {
	GetAllChannels() []*model.Channel
}

// KeyPicker resolves the credential to query with.
type KeyPicker interface {
	NextKey(channelID int64) (*model.Key, error)
}

// BalanceStore is the narrow write surface the monitor needs.
type BalanceStore interface {
	UpdateChannelBalance(channelID int64, balance, currency string, checkedAt time.Time, balanceErr string) error
}

// Monitor refreshes channel balances on a timer.
type Monitor struct {
	cfg   Config
	chans ChannelLister
	keys  KeyPicker
	store BalanceStore

	// query lets tests substitute the upstream call.
	query func(ctx context.Context, providerName, apiKey, baseURL, overrideURL string) (provider.Balance, error)

	stopOnce sync.Once
	stop     chan struct{}
	wg       sync.WaitGroup
}

// New constructs a monitor. A zero Config falls back to DefaultConfig.
func New(cfg Config, chans ChannelLister, keys KeyPicker, st BalanceStore) *Monitor {
	if cfg.Interval == 0 {
		cfg = DefaultConfig
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultConfig.Timeout
	}
	return &Monitor{
		cfg:   cfg,
		chans: chans,
		keys:  keys,
		store: st,
		query: provider.QueryBalance,
		stop:  make(chan struct{}),
	}
}

// SetQuery overrides the upstream query function (tests).
func (m *Monitor) SetQuery(fn func(ctx context.Context, providerName, apiKey, baseURL, overrideURL string) (provider.Balance, error)) {
	if fn != nil {
		m.query = fn
	}
}

// Start runs the refresh loop until Stop is called or ctx is cancelled.
// A non-positive Interval makes it a no-op so callers can start it
// unconditionally.
func (m *Monitor) Start(ctx context.Context) {
	if m.cfg.Interval <= 0 {
		return
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		// First sweep shortly after boot so a restart refreshes stale
		// numbers, but not so fast that it races startup.
		timer := time.NewTimer(m.jittered(m.cfg.Interval))
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stop:
				return
			case <-timer.C:
				m.Sweep(ctx)
				timer.Reset(m.jittered(m.cfg.Interval))
			}
		}
	}()
}

// Stop terminates the loop and waits for it to exit.
func (m *Monitor) Stop() {
	m.stopOnce.Do(func() { close(m.stop) })
	m.wg.Wait()
}

// jittered returns d plus a random +/- Jitter fraction.
func (m *Monitor) jittered(d time.Duration) time.Duration {
	if m.cfg.Jitter <= 0 {
		return d
	}
	// rand.Int63n panics on 0; guard the tiny-duration case.
	span := int64(float64(d) * m.cfg.Jitter)
	if span <= 0 {
		return d
	}
	return d + time.Duration(rand.Int63n(span*2)-span)
}

// Sweep queries every eligible channel once. Errors are per-channel and
// never abort the sweep: one broken upstream must not stop the others.
func (m *Monitor) Sweep(ctx context.Context) {
	channels := m.chans.GetAllChannels()
	refreshed, failed := 0, 0
	for _, ch := range channels {
		if !eligible(ch) {
			continue
		}
		if _, err := m.QueryChannel(ctx, ch); err != nil {
			failed++
			continue
		}
		refreshed++
	}
	if refreshed > 0 || failed > 0 {
		logging.Info("balance sweep complete",
			logging.F("refreshed", refreshed),
			logging.F("failed", failed),
			logging.F("skipped", len(channels)-refreshed-failed),
		)
	}
}

// eligible reports whether a channel can have its balance queried:
// enabled, and the provider declares a balance format (the parser to
// use). A channel-level balance_url only changes *where* the request
// goes, not how the response is parsed, so a provider with no format is
// still unsupported even with an override.
//
// This must stay in lockstep with QueryChannel's own rejection path;
// otherwise a sweep would log an "unsupported" error for every
// ineligible channel on every tick.
func eligible(ch *model.Channel) bool {
	if ch == nil || ch.Status != model.ChannelEnabled {
		return false
	}
	d, ok := provider.LookupProvider(ch.Provider)
	if !ok {
		return false
	}
	return d.Balance.Format != ""
}

// ErrUnsupported is returned by QueryChannel for channels that cannot
// have their balance queried.
var ErrUnsupported = provider.ErrBalanceUnsupported

// QueryChannel refreshes one channel synchronously and persists the
// result. A failed query records the error and returns it; the previous
// balance stays visible.
func (m *Monitor) QueryChannel(ctx context.Context, ch *model.Channel) (provider.Balance, error) {
	if ch == nil {
		return provider.Balance{}, errors.New("balance: nil channel")
	}
	ch = m.reload(ch)

	if !eligible(ch) {
		return provider.Balance{}, ErrUnsupported
	}

	// Recover from a panicking parser so one bad upstream response
	// cannot take down the sweep goroutine.
	var (
		b   provider.Balance
		err error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("balance: panic querying channel %d: %v", ch.ID, r)
			}
		}()
		b, err = m.queryWithKey(ctx, ch)
	}()

	now := time.Now().UTC()
	if err != nil {
		if rerr := m.store.UpdateChannelBalance(ch.ID, "", "", now, err.Error()); rerr != nil {
			logging.Warn("balance: persist error failed",
				logging.F("channel_id", ch.ID), logging.F("error", rerr.Error()))
		}
		return provider.Balance{}, err
	}

	// Format with the upstream's own precision: money is stored and
	// displayed as a string so 110.00 does not become 110.
	value := formatAmount(b.Available)
	if rerr := m.store.UpdateChannelBalance(ch.ID, value, b.Currency, now, ""); rerr != nil {
		return provider.Balance{}, fmt.Errorf("balance: persist: %w", rerr)
	}
	ch.Balance = value
	ch.BalanceCurrency = b.Currency
	ch.BalanceUpdatedAt = &now
	ch.BalanceError = ""
	return b, nil
}

// queryWithKey resolves the credential and dispatches to the parser.
func (m *Monitor) queryWithKey(ctx context.Context, ch *model.Channel) (provider.Balance, error) {
	key, err := m.keys.NextKey(ch.ID)
	if err != nil || key == nil || key.Key == "" {
		return provider.Balance{}, errors.New("balance: no usable key for channel")
	}

	qctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()

	// one-api/new-api relays need a two-document read, so they take a
	// dedicated path rather than the generic single-GET dispatch.
	if formatIsOneAPI(ch) {
		return provider.QueryOneAPIBalance(qctx, key.Key, ch.BaseURL, ch.BalanceURL,
			time.Now().AddDate(0, 0, -90).Unix())
	}
	return m.query(qctx, ch.Provider, key.Key, ch.BaseURL, ch.BalanceURL)
}

// formatIsOneAPI reports whether this channel uses the one-api billing
// pair (which lives under /dashboard/billing/ rather than a single
// balance endpoint).
func formatIsOneAPI(ch *model.Channel) bool {
	if ch.BalanceURL != "" && strings.Contains(ch.BalanceURL, "/dashboard/billing/") {
		return true
	}
	if d, ok := provider.LookupProvider(ch.Provider); ok {
		return d.Balance.Format == "oneapi"
	}
	return false
}

// reload fetches the channel's current row so a sweep is not working
// from a stale pool snapshot (the pool is rebuilt on admin writes, but
// the monitor may hold a slice from before that).
func (m *Monitor) reload(ch *model.Channel) *model.Channel {
	for _, live := range m.chans.GetAllChannels() {
		if live.ID == ch.ID {
			return live
		}
	}
	return ch
}

// FormatAmount renders a balance with up to 4 decimals, trimming
// trailing zeros so "110.00" stays "110.00" but 110.0 becomes "110".
// Exported so the admin tests' fake monitor persists snapshots in the
// same format the real one does.
func FormatAmount(v float64) string {
	s := strconv.FormatFloat(v, 'f', 4, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// formatAmount is the internal alias kept for readability at call sites.
func formatAmount(v float64) string { return FormatAmount(v) }
