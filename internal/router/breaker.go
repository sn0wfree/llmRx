package router

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

const (
	defaultMaxFailures = 5
	defaultResetDur    = 60 * time.Second

	// rateLimitCooldown is how long a 429 (rate-limited) channel is
	// kept out of the candidate set before being retried.
	rateLimitCooldown = 5 * time.Second

	// minuteWindowRateThreshold is the failure fraction over the
	// last minute that trips the sliding-window cooldown.
	minuteWindowRateThreshold = 0.5
	// minuteWindowMinSamples is the minimum number of observations
	// in the window before the rate threshold applies (a 1-of-2
	// flake must not take a channel down).
	minuteWindowMinSamples = 10
	// windowSize is the ring buffer of recent attempts used to
	// compute the minute failure rate. Bounded memory per channel.
	windowSize = 64
)

// windowSample is one observed attempt inside the minute-rate ring.
type windowSample struct {
	at   int64 // unix milliseconds
	fail bool
}

type breakerEntry struct {
	failures    int
	lastFailure time.Time
	isOpen      bool
	// hardReject marks a channel that returned 401/404: retrying is
	// pointless (auth/config error), so it stays excluded until the
	// operator reloads or a success arrives.
	hardReject bool
	// cooldownUntil parks the channel for rateLimitCooldown (429)
	// or minuteCooldown (sustained minute failure rate).
	cooldownUntil time.Time
	// window is a ring of recent attempts for the minute failure
	// rate computation.
	window    [windowSize]windowSample
	windowN   int // number of samples stored
	windowPos int // next write slot
	mu        sync.Mutex
}

// BreakerDefaults is the live snapshot of operator-tunable
// defaults (admin /runtime config). When a channel's per-row
// circuit-breaker config is unset, the breaker falls back to
// these values, so changes via the admin UI take effect on the
// next request without a restart.
type BreakerDefaults interface {
	BreakerMaxFailures() int64
	BreakerResetTimeoutMs() int64
}

// breakerCfg is the resolved per-channel circuit-breaker config:
// maxFailures + resetDur. Stored in a per-channel cache so the hot
// path never hits the store.
type breakerCfg struct {
	maxFailures int
	resetDur    time.Duration
}

type CircuitBreaker struct {
	store    BreakerStore
	defaults BreakerDefaults // optional; nil ⇒ built-in defaults

	// cachedDefaults is the resolved defaults snapshot, refreshed
	// when SetDefaults is called or when the operator updates
	// runtime config. Stored as a value so reads are atomic and
	// lock-free.
	cachedDefaults atomic.Value // *breakerCfg

	// cfgCache holds per-channel breakerCfg entries. The snapshot
	// is read under cfgMu RLock; reload() rebuilds a single entry
	// under cfgMu Lock. The map is keyed by channelID and never
	// shrinks (channels are bounded by operator config).
	cfgCache map[int64]breakerCfg
	cfgMu    sync.RWMutex

	// entries are the per-channel breaker state machines, keyed by
	// channelID. sync.Map provides lock-free reads on the hot path
	// (Filter, RecordSuccess, RecordFailure).
	entries sync.Map
}

// BreakerStore is the narrow contract the breaker actually depends
// on: a single lookup for a channel's circuit-breaker config. The
// production code passes a *store.SQLite; tests pass a stub.
type BreakerStore interface {
	GetChannel(id int64) (*model.Channel, error)
}

func NewCircuitBreaker(st store.Store) *CircuitBreaker {
	b := &CircuitBreaker{
		store:    st,
		cfgCache: make(map[int64]breakerCfg),
	}
	// Seed cachedDefaults with built-in values.
	def := breakerCfg{maxFailures: defaultMaxFailures, resetDur: defaultResetDur}
	b.cachedDefaults.Store(&def)
	return b
}

// SetDefaults injects the live defaults source. nil disables
// the live-source path and the breaker falls back to its built-in
// constants.
func (b *CircuitBreaker) SetDefaults(d BreakerDefaults) {
	b.defaults = d
	b.refreshDefaults()
}

// refreshDefaults rebuilds the cachedDefaults snapshot from the
// live defaults source (or the built-in constants).
func (b *CircuitBreaker) refreshDefaults() {
	maxFail := int64(defaultMaxFailures)
	resetMs := int64(defaultResetDur / time.Millisecond)
	if b.defaults != nil {
		maxFail = b.defaults.BreakerMaxFailures()
		resetMs = b.defaults.BreakerResetTimeoutMs()
	}
	if maxFail <= 0 {
		maxFail = defaultMaxFailures
	}
	resetDur := time.Duration(resetMs) * time.Millisecond
	if resetDur <= 0 {
		resetDur = defaultResetDur
	}
	// Cache the resolved snapshot.
	def := breakerCfg{maxFailures: int(maxFail), resetDur: resetDur}
	b.cachedDefaults.Store(&def)
	// Per-channel overrides still need recomputation; clear the
	// cache so cfgFor() re-reads them from the store on next access.
	b.cfgMu.Lock()
	b.cfgCache = make(map[int64]breakerCfg)
	b.cfgMu.Unlock()
}

// cfgFor resolves the per-channel breaker config. Hot path: a single
// RLock + map lookup. Cold path (first call for a channel): one
// SQL GetChannel() lookup, then cached.
func (b *CircuitBreaker) cfgFor(channelID int64) (int, time.Duration) {
	if b.cfgCache != nil {
		b.cfgMu.RLock()
		cfg, ok := b.cfgCache[channelID]
		b.cfgMu.RUnlock()
		if ok {
			return cfg.maxFailures, cfg.resetDur
		}
	}

	// Cold path: read from the store and populate the cache.
	def := b.resolveDefaults()
	maxFail := def.maxFailures
	resetDur := def.resetDur
	if b.store != nil {
		if ch, err := b.store.GetChannel(channelID); err == nil && ch != nil {
			if ch.CircuitBreaker.MaxFailures > 0 {
				maxFail = int(ch.CircuitBreaker.MaxFailures)
			}
			if ch.CircuitBreaker.ResetTimeout > 0 {
				resetDur = ch.CircuitBreaker.ResetTimeout
			}
		}
	}
	if b.cfgCache != nil {
		b.cfgMu.Lock()
		// Double-check: another goroutine may have populated while
		// we were unlocked. Cheap to overwrite with the same value.
		b.cfgCache[channelID] = breakerCfg{maxFailures: maxFail, resetDur: resetDur}
		b.cfgMu.Unlock()
	}
	return maxFail, resetDur
}

// resolveDefaults returns the cached defaults snapshot, falling
// back to the live defaults source (or built-in constants if both
// are uninitialized). Test code constructs CircuitBreaker{} directly
// without NewCircuitBreaker, so we read b.defaults on demand.
func (b *CircuitBreaker) resolveDefaults() breakerCfg {
	if defv := b.cachedDefaults.Load(); defv != nil {
		return *defv.(*breakerCfg)
	}
	// No cached snapshot — synthesize one from b.defaults (the live
	// source) or built-in constants. This path is hit only on cold
	// construction (NewCircuitBreaker seeds the cache immediately).
	maxFail := int64(defaultMaxFailures)
	resetMs := int64(defaultResetDur / time.Millisecond)
	if b.defaults != nil {
		maxFail = b.defaults.BreakerMaxFailures()
		resetMs = b.defaults.BreakerResetTimeoutMs()
	}
	if maxFail <= 0 {
		maxFail = defaultMaxFailures
	}
	resetDur := time.Duration(resetMs) * time.Millisecond
	if resetDur <= 0 {
		resetDur = defaultResetDur
	}
	return breakerCfg{maxFailures: int(maxFail), resetDur: resetDur}
}

func (b *CircuitBreaker) getEntry(channelID int64) *breakerEntry {
	// Fast path: load without allocating. After warmup, every
	// channel has an entry and this is a lock-free sync.Map read.
	if v, ok := b.entries.Load(channelID); ok {
		return v.(*breakerEntry)
	}
	// Slow path: first-time lookup for this channel. Allocate
	// the entry and try to publish; if another goroutine beat us
	// to it, LoadOrStore returns the existing one and our
	// freshly-allocated entry is dropped (garbage-collected).
	entry := &breakerEntry{}
	actual, _ := b.entries.LoadOrStore(channelID, entry)
	return actual.(*breakerEntry)
}

func (b *CircuitBreaker) reload(channelID int64) {
	entry := b.getEntry(channelID)
	entry.mu.Lock()
	entry.failures = 0
	entry.isOpen = false
	entry.hardReject = false
	entry.cooldownUntil = time.Time{}
	entry.mu.Unlock()
	// Also clear the cached cfg so the next cfgFor() picks up any
	// updated operator config (per-channel or defaults).
	b.cfgMu.Lock()
	delete(b.cfgCache, channelID)
	b.cfgMu.Unlock()
	b.refreshDefaults()
}

// reloadAll clears every breaker's state, returning every channel
// to the closed position. Called by admin /reload.
func (b *CircuitBreaker) reloadAll() {
	b.entries.Range(func(key, _ any) bool {
		b.entries.Delete(key)
		return true
	})
	b.cfgMu.Lock()
	b.cfgCache = make(map[int64]breakerCfg)
	b.cfgMu.Unlock()
	b.refreshDefaults()
}

func (b *CircuitBreaker) Filter(channels []*model.Channel) []*model.Channel {
	now := time.Now()
	var healthy []*model.Channel
	for _, ch := range channels {
		entry := b.getEntry(ch.ID)
		entry.mu.Lock()
		// Hard rejects (401/404) stay excluded until reload or a
		// recorded success — retrying an auth failure is pointless.
		if entry.hardReject {
			entry.mu.Unlock()
			continue
		}
		// Short cooldowns (429).
		if now.Before(entry.cooldownUntil) {
			entry.mu.Unlock()
			continue
		}
		// Sustained bad minute failure rate: exclude while the
		// sliding window looks unhealthy (pure window semantics —
		// success samples push it back below the threshold).
		if fails, total := entry.minuteFailureRate(now); total >= minuteWindowMinSamples && float64(fails)/float64(total) > minuteWindowRateThreshold {
			entry.mu.Unlock()
			continue
		}
		if entry.isOpen {
			_, resetDur := b.cfgFor(ch.ID)
			if time.Since(entry.lastFailure) > resetDur {
				entry.isOpen = false
				entry.failures = 0
				healthy = append(healthy, ch)
			}
			entry.mu.Unlock()
			continue
		}
		healthy = append(healthy, ch)
		entry.mu.Unlock()
	}
	return healthy
}

func (b *CircuitBreaker) RecordSuccess(channelID int64) {
	entry := b.getEntry(channelID)
	entry.mu.Lock()
	entry.failures = 0
	entry.isOpen = false
	entry.hardReject = false
	entry.cooldownUntil = time.Time{}
	entry.pushWindow(time.Now(), false)
	entry.mu.Unlock()
}

// pushWindow appends one attempt to the minute-rate ring. Callers
// must hold entry.mu.
func (e *breakerEntry) pushWindow(now time.Time, fail bool) {
	e.window[e.windowPos] = windowSample{at: now.UnixMilli(), fail: fail}
	e.windowPos = (e.windowPos + 1) % windowSize
	if e.windowN < windowSize {
		e.windowN++
	}
}

// minuteFailureRate computes the failure fraction over the last 60
// seconds from the ring. Callers must hold entry.mu.
func (e *breakerEntry) minuteFailureRate(now time.Time) (failures, total int) {
	cutoff := now.Add(-time.Minute).UnixMilli()
	for i := 0; i < e.windowN; i++ {
		s := e.window[i]
		if s.at >= cutoff {
			total++
			if s.fail {
				failures++
			}
		}
	}
	return failures, total
}

// RecordFailure records a failed upstream attempt bucketed by the
// upstream HTTP status:
//
//   - 401/404: hard reject — the channel is excluded until an
//     operator reload or a success (retrying is pointless).
//   - 429: short 5s cooldown; does not count toward the consecutive
//     failure counter (transient rate limiting, not a quality drop).
//   - other (5xx and network errors): existing consecutive-failure
//     semantics.
//
// In addition, every failure updates the minute sliding window; a
// channel whose failure rate over the last minute exceeds 50% (with
// enough samples) is parked for a minute.
func (b *CircuitBreaker) RecordFailure(channelID int64, status int) {
	entry := b.getEntry(channelID)
	entry.mu.Lock()
	now := time.Now()

	switch status {
	case http.StatusUnauthorized, http.StatusNotFound:
		entry.hardReject = true
		entry.failures = 0
		entry.isOpen = false
		entry.cooldownUntil = time.Time{}
		entry.pushWindow(now, true)
		entry.mu.Unlock()
		return
	case http.StatusTooManyRequests:
		entry.cooldownUntil = now.Add(rateLimitCooldown)
		entry.pushWindow(now, true)
		entry.mu.Unlock()
		return
	}

	entry.failures++
	entry.lastFailure = now
	maxFail, _ := b.cfgFor(channelID)
	if entry.failures >= maxFail {
		entry.isOpen = true
	}
	entry.pushWindow(now, true)
	entry.mu.Unlock()
}
