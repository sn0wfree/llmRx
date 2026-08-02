// Package prober maintains a periodic health probe cache for every
// enabled channel. A lightweight ListModels call exercises the
// upstream's network path and auth, surfacing DNS / TLS / auth
// failures without burning a real chat quota. The auto routing path
// consults Healthy() before sending the actual request, so a probe
// failure fails fast instead of paying the full upstream latency.
//
// Cadence strategy (controlled by Config):
//   - Interval:    background loop tick (default 30s). Each tick
//                  probes only channels whose NextProbeAt is due.
//   - HealthyInterval: how long after a successful probe to wait
//                  before re-probing (default 5m — healthy channels
//                  don't need to be polled every 30s).
//   - Backoff:     on failure, NextProbeAt doubles each miss up to
//                  MaxBackoff (default 10m). A channel that's been
//                  failing for an hour is checked once every 10m
//                  instead of every 30s.
//   - TTL:         freshness window for Healthy() lookups (default
//                  60s). Stale entries fall back to "assume healthy"
//                  so a transient blip doesn't black-hole traffic.
//
// Net effect: a 10-channel deployment probes ~12 times per hour per
// channel while healthy (≈120/hour total), vs. the naive "every 30s"
// approach which would emit 1200/hour.
package prober

import (
	"context"
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/provider"
)

// Config controls probe cadence. Zero values fall back to defaults
// so callers can leave fields unset.
type Config struct {
	// Interval is the background loop tick. Each tick only
	// probes channels whose NextProbeAt has come due.
	Interval time.Duration // default 30s
	// Timeout is the per-channel HTTP timeout for one probe.
	Timeout time.Duration // default 5s
	// TTL is how long a cached result is considered fresh for
	// Healthy() lookups. Stale entries default to "healthy"
	// so traffic keeps flowing during probe outages.
	TTL time.Duration // default 60s
	// HealthyInterval is how long after a successful probe to
	// wait before re-probing. Set >= Interval.
	HealthyInterval time.Duration // default 5m
	// MaxBackoff caps the exponential backoff applied to
	// repeatedly-failing channels.
	MaxBackoff time.Duration // default 10m
	// RecentTrafficWindow is the freshness window for real
	// traffic signals (LastSuccessAt). Channels that received a
	// successful real request within this window are skipped by
	// the probe tick — real usage is a better health signal.
	RecentTrafficWindow time.Duration // default 5m
}

func (c *Config) applyDefaults() {
	if c.Interval <= 0 {
		c.Interval = 30 * time.Second
	}
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Second
	}
	if c.TTL <= 0 {
		c.TTL = 60 * time.Second
	}
	if c.HealthyInterval <= 0 {
		c.HealthyInterval = 5 * time.Minute
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 10 * time.Minute
	}
	if c.RecentTrafficWindow <= 0 {
		c.RecentTrafficWindow = 5 * time.Minute
	}
}

// Result captures the outcome of one probe attempt and the most
// recent real-traffic signal. The prober skips channels that
// received successful real traffic within the freshness window —
// real usage is a better health signal than a synthetic probe.
type Result struct {
	OK          bool
	Error       string
	CheckedAt   time.Time
	NextProbeAt time.Time // scheduled next attempt; loop skips if future
	// LastSuccessAt records the latest successful real request
	// through this channel (NOT a probe success). Used by tick()
	// to skip probes on busy channels.
	LastSuccessAt time.Time
	// ConsecFails counts consecutive failures so backoff can
	// double on each miss up to MaxBackoff.
	ConsecFails int
	// ConsecRealFails counts consecutive failures observed in
	// real (non-probe) traffic. A real failure forces an
	// immediate re-probe so we don't wait the full backoff
	// window to notice the channel is genuinely down.
	ConsecRealFails int
}

// Cache is a thread-safe channel-id → latest-probe-result map. A
// background loop refreshes entries; Healthy() returns whether the
// latest entry is recent and successful.
type Cache struct {
	cfg    Config
	store  channelLister
	pool   keyPicker
	mu     sync.RWMutex
	latest map[int64]Result

	stop chan struct{}
	once sync.Once
}

// channelLister is the narrow slice of the store we depend on; the
// concrete *store.SQLite satisfies it.
type channelLister interface {
	GetChannels() ([]model.Channel, error)
}

// keyPicker is the narrow slice of pool we depend on.
type keyPicker interface {
	NextKey(channelID int64) (*model.Key, error)
}

// New constructs a Cache and starts the background loop. Pass nil
// for cfg to use defaults. The returned Cache must be closed with
// Stop() during graceful shutdown.
func New(cfg Config, st channelLister, p keyPicker) *Cache {
	cfg.applyDefaults()
	c := &Cache{
		cfg:    cfg,
		store:  st,
		pool:   p,
		latest: make(map[int64]Result),
		stop:   make(chan struct{}),
	}
	logging.Info("prober: starting",
		logging.F("interval", cfg.Interval),
		logging.F("healthy_interval", cfg.HealthyInterval),
		logging.F("max_backoff", cfg.MaxBackoff),
		logging.F("recent_traffic_window", cfg.RecentTrafficWindow),
		logging.F("ttl", cfg.TTL),
	)
	go c.loop()
	return c
}

// Stop terminates the background loop. Safe to call multiple times.
func (c *Cache) Stop() {
	c.once.Do(func() { close(c.stop) })
}

// Healthy reports whether channelID was probed successfully within
// the freshness TTL. Channels that have never been probed (cold
// cache) are treated as healthy so the first auto request isn't
// delayed by a cache warm-up. Stale entries also default to
// healthy — better to attempt than to block traffic on stale data.
func (c *Cache) Healthy(channelID int64) bool {
	c.mu.RLock()
	r, ok := c.latest[channelID]
	c.mu.RUnlock()
	if !ok {
		return true
	}
	if time.Since(r.CheckedAt) > c.cfg.TTL {
		return true
	}
	return r.OK
}

// Latest returns the most recent probe result for channelID. Used
// by the admin UI to show "last probe status" per channel.
func (c *Cache) Latest(channelID int64) (Result, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.latest[channelID]
	return r, ok
}

// Snapshot returns a copy of all known probe results. Used by tests.
func (c *Cache) Snapshot() map[int64]Result {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[int64]Result, len(c.latest))
	for k, v := range c.latest {
		out[k] = v
	}
	return out
}

// ProbeAll runs one full probe pass over every enabled channel.
// Exposed so callers (admin endpoint, tests) can force a refresh.
func (c *Cache) ProbeAll(ctx context.Context) {
	chs, err := c.store.GetChannels()
	if err != nil {
		logging.Warn("prober: list channels", logging.F("err", err.Error()))
		return
	}
	for i := range chs {
		ch := &chs[i]
		if ch.Status != model.ChannelEnabled {
			continue
		}
		c.ProbeChannel(ctx, ch)
	}
}

// ProbeChannel runs one probe against a single channel and updates
// the cache. Errors are recorded but never returned — probes must
// never propagate failures.
func (c *Cache) ProbeChannel(ctx context.Context, ch *model.Channel) {
	if c.pool == nil {
		return
	}
	key, err := c.pool.NextKey(ch.ID)
	if err != nil {
		c.record(ch.ID, Result{
			OK:        false,
			Error:     "no key: " + err.Error(),
			CheckedAt: time.Now(),
		})
		return
	}
	ctx2, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	_, err = provider.ListModels(ctx2, ch.Provider, key.Key, ch.BaseURL)
	now := time.Now()
	if err != nil {
		logging.Warn("prober: probe failed",
			logging.F("channel_id", ch.ID),
			logging.F("channel", ch.Name),
			logging.F("provider", ch.Provider),
			logging.F("error", err.Error()),
		)
	} else {
		logging.Debug("prober: probe success",
			logging.F("channel_id", ch.ID),
			logging.F("channel", ch.Name),
		)
	}
	if err != nil {
		// Failure: bump ConsecFails and schedule next probe with
		// exponential backoff. The new attempt window is clamped to
		// the last attempt + MaxBackoff so a long-broken channel
		// doesn't get hammered.
		c.mu.Lock()
		last := c.latest[ch.ID]
		consec := last.ConsecFails + 1
		delay := c.cfg.Interval
		for i := 1; i < consec && delay < c.cfg.MaxBackoff; i++ {
			delay *= 2
		}
		if delay > c.cfg.MaxBackoff {
			delay = c.cfg.MaxBackoff
		}
		c.latest[ch.ID] = Result{
			OK:          false,
			Error:       err.Error(),
			CheckedAt:   now,
			NextProbeAt: now.Add(delay),
			ConsecFails: consec,
		}
		c.mu.Unlock()
		logging.Debug("prober: next probe scheduled (backoff)",
			logging.F("channel_id", ch.ID),
			logging.F("consec_fails", consec),
			logging.F("next_in", delay),
		)
		return
	}
	// Success: reset ConsecFails and schedule next probe at
	// HealthyInterval from now. This is the headline optimisation
	// — healthy channels are polled ~10x less often than the naive
	// loop.
	c.record(ch.ID, Result{
		OK:          true,
		CheckedAt:   now,
		NextProbeAt: now.Add(c.cfg.HealthyInterval),
		ConsecFails: 0,
	})
	logging.Debug("prober: next probe scheduled (healthy)",
		logging.F("channel_id", ch.ID),
		logging.F("next_in", c.cfg.HealthyInterval),
	)
}

func (c *Cache) record(channelID int64, r Result) {
	c.mu.Lock()
	c.latest[channelID] = r
	c.mu.Unlock()
}

// RecordForTest lets tests inject a synthetic result without
// performing an actual probe. Not part of the public API.
func (c *Cache) RecordForTest(channelID int64, r Result) {
	c.record(channelID, r)
}

// RecordSuccess is called by the routing layer after a real (non-
// probe) request succeeds through the channel. It marks the
// channel as freshly-validated so the next tick skips its probe.
// If the channel has a failing probe in cache, that stale entry is
// cleared.
func (c *Cache) RecordSuccess(channelID int64) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.latest[channelID]
	r.LastSuccessAt = now
	r.ConsecRealFails = 0
	// If a prior probe was failing, a successful real call proves
	// the channel is fine — clear the failure state and push the
	// next probe out by HealthyInterval (real traffic is enough
	// evidence for now).
	wasFailing := !r.OK
	if wasFailing {
		r.OK = true
		r.Error = ""
		r.ConsecFails = 0
		r.CheckedAt = now
	}
	r.NextProbeAt = now.Add(c.cfg.HealthyInterval)
	c.latest[channelID] = r
	if wasFailing {
		logging.Info("prober: real success clears probe failure",
			logging.F("channel_id", channelID),
			logging.F("prior_error", r.Error),
		)
	}
}

// RecordFailure is called by the routing layer after a real (non-
// probe) request fails through the channel. It bumps the real-
// failure counter and forces an immediate probe on the next tick
// (NextProbeAt set to now) so we don't wait the full backoff
// window to react.
func (c *Cache) RecordFailure(channelID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.latest[channelID]
	r.ConsecRealFails++
	r.OK = false
	r.NextProbeAt = time.Now() // immediate probe on next tick
	c.latest[channelID] = r
	logging.Warn("prober: real request failure",
		logging.F("channel_id", channelID),
		logging.F("consec_real_fails", r.ConsecRealFails),
	)
}

func (c *Cache) loop() {
	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()
	c.ProbeAll(context.Background())
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			c.tick()
		}
	}
}

// tick walks every enabled channel and probes only the ones whose
// NextProbeAt has come due. Channels with successful real traffic
// within RecentTrafficWindow are also skipped — real usage is a
// better health signal than a synthetic probe, so we save the
// upstream call.
func (c *Cache) tick() {
	chs, err := c.store.GetChannels()
	if err != nil {
		logging.Debug("prober: list channels", logging.F("err", err.Error()))
		return
	}
	now := time.Now()
	for i := range chs {
		ch := &chs[i]
		if ch.Status != model.ChannelEnabled {
			continue
		}
		c.mu.RLock()
		r, ok := c.latest[ch.ID]
		c.mu.RUnlock()
		if ok && r.NextProbeAt.After(now) {
			continue
		}
		if ok && r.LastSuccessAt.After(now.Add(-c.cfg.RecentTrafficWindow)) {
			logging.Debug("prober: skip (recent real success)",
				logging.F("channel_id", ch.ID),
				logging.F("last_success_ago", now.Sub(r.LastSuccessAt).Truncate(time.Second)),
			)
			continue
		}
		c.ProbeChannel(context.Background(), ch)
	}
}

// Compile-time guarantee that *pool.ChannelPool satisfies keyPicker.
var _ keyPicker = (*pool.ChannelPool)(nil)

// RouterObserver adapts *Cache to the router.TrafficObserver
// interface so the routing engine can notify the prober of every
// real (non-probe) call outcome without importing prober directly.
// Defined here (not in router) to keep the dependency direction
// prober → router one-way.
type RouterObserver struct{ Cache *Cache }

// NewRouterObserver returns an adapter wrapping c.
func NewRouterObserver(c *Cache) *RouterObserver { return &RouterObserver{Cache: c} }

// OnRealSuccess records a successful real call. The prober uses
// this to mark the channel as freshly-validated so the next tick
// skips its probe.
func (o *RouterObserver) OnRealSuccess(channelID int64) {
	if o != nil && o.Cache != nil {
		o.Cache.RecordSuccess(channelID)
	}
}

// OnRealFailure records a failed real call. The prober uses this
// to schedule an immediate re-probe on the next tick.
func (o *RouterObserver) OnRealFailure(channelID int64) {
	if o != nil && o.Cache != nil {
		o.Cache.RecordFailure(channelID)
	}
}
