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
	"github.com/sn0wfree/llmRx/internal/store"
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
	// Per-key writes (P14 phase 4). A store that only implements the
	// channel-level method still works — keysFor then reports the
	// selected key as the only one.
	UpdateKeyBalance(keyID int64, balance, currency string, checkedAt time.Time, balanceErr string) error
	ClearKeyBalance(keyID int64) error
}

// KeyLister supplies a channel's keys so every account can be queried.
// Optional: when unset the monitor falls back to the pool's round-robin
// key, which is the pre-phase-4 behaviour.
type KeyLister interface {
	GetKeys(channelID int64) ([]model.Key, error)
}

// HistoryStore persists the balance time series (P14 phase 3). Kept a
// separate interface because it is optional: a monitor wired without it
// still refreshes the live snapshot, it just records no history.
type HistoryStore interface {
	AppendBalancePoint(channelID int64, at time.Time, balance, currency string) error
	LastBalancePoint(channelID int64) (*store.BalancePoint, error)
	PruneBalanceHistory(cutoff time.Time) (int64, error)
}

// historyDedupWindow suppresses a second point for the same channel
// within this duration. A manual refresh seconds after an automatic
// sweep would otherwise put two dots on the chart a hair apart.
const historyDedupWindow = 10 * time.Minute

// Monitor refreshes channel balances on a timer.
type Monitor struct {
	cfg   Config
	chans ChannelLister
	keys  KeyPicker
	store BalanceStore
	// keyLister supplies every key of a channel so each account can be
	// queried (P14 phase 4). nil falls back to the pool's round-robin
	// pick, i.e. the pre-phase-4 behaviour.
	keyLister KeyLister
	// history is optional: nil means "record no time series". Set via
	// SetHistoryStore.
	history HistoryStore
	// retentionDays returns the history window read live each sweep, so
	// an admin change applies without a restart. nil = 90 days.
	retentionDays func() int

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

// SetHistoryStore enables balance time-series recording.
func (m *Monitor) SetHistoryStore(h HistoryStore) { m.history = h }

// SetKeyLister enables per-key balance queries.
func (m *Monitor) SetKeyLister(kl KeyLister) { m.keyLister = kl }

// keysFor returns the keys to query for a channel. Without a key lister
// it degrades to the pool's round-robin pick, which preserves the old
// single-key behaviour.
func (m *Monitor) keysFor(ch *model.Channel) ([]model.Key, error) {
	if m.keyLister != nil {
		keys, err := m.keyLister.GetKeys(ch.ID)
		if err != nil {
			return nil, fmt.Errorf("balance: list keys: %w", err)
		}
		return keys, nil
	}
	k, err := m.keys.NextKey(ch.ID)
	if err != nil || k == nil {
		return nil, errors.New("balance: no usable key for channel")
	}
	return []model.Key{*k}, nil
}

// queryOneKey runs one upstream query with panic recovery, so a bad
// parser response on one key cannot take down the sweep.
func (m *Monitor) queryOneKey(ctx context.Context, ch *model.Channel, key string) (b provider.Balance, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("balance: panic querying channel %d: %v", ch.ID, r)
		}
	}()

	timeout := m.cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultConfig.Timeout
	}
	qctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if formatIsOneAPI(ch) {
		return provider.QueryOneAPIBalance(qctx, key, ch.BaseURL, ch.BalanceURL,
			time.Now().AddDate(0, 0, -90).Unix())
	}
	return m.query(qctx, ch.Provider, key, ch.BaseURL, ch.BalanceURL)
}

// clearKeyBalance resets a non-active key's balance columns.
func (m *Monitor) clearKeyBalance(k *model.Key) {
	// Only touch rows that actually carry a value: a sweep over many
	// disabled keys should not issue an UPDATE each.
	if k.Balance == "" && k.BalanceError == "" {
		return
	}
	if err := m.store.ClearKeyBalance(k.ID); err != nil {
		logging.Warn("balance: clear key balance failed",
			logging.F("key_id", k.ID), logging.F("error", err.Error()))
	}
	k.Balance = ""
	k.BalanceCurrency = ""
	k.BalanceUpdatedAt = nil
	k.BalanceError = ""
}

// SetRetentionDays installs the live history-window provider.
func (m *Monitor) SetRetentionDays(fn func() int) { m.retentionDays = fn }

// historyRetention resolves the effective retention window.
func (m *Monitor) historyRetention() time.Duration {
	days := 90
	if m.retentionDays != nil {
		if d := m.retentionDays(); d > 0 {
			days = d
		}
	}
	return time.Duration(days) * 24 * time.Hour
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

	// Retention runs with the sweep rather than on its own timer: an
	// hourly sweep is frequent enough that a reduced window takes effect
	// promptly, and one goroutine is easier to reason about than two.
	m.PruneHistory(time.Now().UTC())
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

	now := time.Now().UTC()

	// Query every active key, not just the first. Balance is account-
	// level, so a channel holding several keys has several balances; the
	// channel-level number reported below is the minimum of them.
	keys, kerr := m.keysFor(ch)
	if kerr != nil {
		if rerr := m.store.UpdateChannelBalance(ch.ID, "", "", now, kerr.Error()); rerr != nil {
			logging.Warn("balance: persist error failed",
				logging.F("channel_id", ch.ID), logging.F("error", rerr.Error()))
		}
		return provider.Balance{}, kerr
	}

	var (
		best      provider.Balance // lowest successful balance
		bestFound bool
		lastErr   error
		queried   int
	)
	for i := range keys {
		k := &keys[i]
		if k.Status != model.KeyActive || k.Key == "" {
			// A non-active key will never be used again; keeping its old
			// balance would read as if the account were still in play.
			m.clearKeyBalance(k)
			continue
		}
		b, err := m.queryOneKey(ctx, ch, k.Key)
		queried++
		if err != nil {
			lastErr = err
			if uerr := m.store.UpdateKeyBalance(k.ID, "", "", now, err.Error()); uerr != nil {
				logging.Warn("balance: persist key error failed",
					logging.F("key_id", k.ID), logging.F("error", uerr.Error()))
			}
			continue
		}
		value := formatAmount(b.Available)
		if uerr := m.store.UpdateKeyBalance(k.ID, value, b.Currency, now, ""); uerr != nil {
			logging.Warn("balance: persist key balance failed",
				logging.F("key_id", k.ID), logging.F("error", uerr.Error()))
		}
		if !bestFound || b.Available < best.Available {
			best = b
			bestFound = true
		}
	}

	if !bestFound {
		// Every key failed (or there were no usable keys). Keep the
		// channel's previous value: losing the last known number is worse
		// than a stale one.
		msg := "no usable key"
		if lastErr != nil {
			msg = lastErr.Error()
		}
		if rerr := m.store.UpdateChannelBalance(ch.ID, "", "", now, msg); rerr != nil {
			logging.Warn("balance: persist error failed",
				logging.F("channel_id", ch.ID), logging.F("error", rerr.Error()))
		}
		if lastErr != nil {
			return provider.Balance{}, lastErr
		}
		return provider.Balance{}, errors.New("balance: no usable key for channel")
	}

	// Format with the upstream's own precision: money is stored and
	// displayed as a string so 110.00 does not become 110.
	value := formatAmount(best.Available)
	if rerr := m.store.UpdateChannelBalance(ch.ID, value, best.Currency, now, ""); rerr != nil {
		return provider.Balance{}, fmt.Errorf("balance: persist: %w", rerr)
	}
	ch.Balance = value
	ch.BalanceCurrency = best.Currency
	ch.BalanceUpdatedAt = &now
	ch.BalanceError = ""
	_ = lastErr

	// History is written only on success: a failed query records
	// balance_error, but must not insert a row. "No data" and "balance
	// is zero" are different facts, and conflating them draws a cliff
	// into the trend chart that never happened.
	m.recordHistory(ch, now, value, best.Currency)

	return best, nil
}

// recordHistory appends a time-series point, honouring the dedup window.
//
// Failures are logged and swallowed: the live snapshot is the primary
// record, and history falling behind must never turn a successful
// balance refresh into an error the caller has to handle.
func (m *Monitor) recordHistory(ch *model.Channel, at time.Time, balance, currency string) {
	if m.history == nil {
		return
	}
	// Dedup: a manual refresh seconds after an automatic sweep would
	// otherwise put two points a hair apart on the chart.
	if last, err := m.history.LastBalancePoint(ch.ID); err == nil && last != nil {
		if at.Sub(last.TS) < historyDedupWindow {
			return
		}
	}
	if err := m.history.AppendBalancePoint(ch.ID, at, balance, currency); err != nil {
		logging.Warn("balance: record history failed",
			logging.F("channel_id", ch.ID), logging.F("error", err.Error()))
	}
}

// PruneHistory deletes points outside the retention window. Called from
// the sweep so a reduced window takes effect within one interval.
func (m *Monitor) PruneHistory(now time.Time) {
	if m.history == nil {
		return
	}
	cutoff := now.Add(-m.historyRetention())
	n, err := m.history.PruneBalanceHistory(cutoff)
	if err != nil {
		logging.Warn("balance: prune history failed", logging.F("error", err.Error()))
		return
	}
	if n > 0 {
		logging.Info("balance history pruned",
			logging.F("rows", n), logging.F("cutoff", cutoff.UTC().Format(time.RFC3339)))
	}
}

// queryWithKey resolves the credential and dispatches to the parser.
func (m *Monitor) queryWithKey(ctx context.Context, ch *model.Channel) (provider.Balance, error) {
	key, err := m.keys.NextKey(ch.ID)
	if err != nil || key == nil || key.Key == "" {
		return provider.Balance{}, errors.New("balance: no usable key for channel")
	}
	return m.queryOneKey(ctx, ch, key.Key)
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
