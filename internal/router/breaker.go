package router

import (
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

const (
	defaultMaxFailures = 5
	defaultResetDur    = 60 * time.Second
)

type breakerEntry struct {
	failures    int
	lastFailure time.Time
	isOpen      bool
	mu          sync.Mutex
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

type CircuitBreaker struct {
	store    BreakerStore
	defaults BreakerDefaults // optional; nil ⇒ built-in defaults
	entries  map[int64]*breakerEntry
	mu       sync.RWMutex
}

// BreakerStore is the narrow contract the breaker actually depends
// on: a single lookup for a channel's circuit-breaker config. The
// production code passes a *store.SQLite; tests pass a stub.
type BreakerStore interface {
	GetChannel(id int64) (*model.Channel, error)
}

func NewCircuitBreaker(st store.Store) *CircuitBreaker {
	return &CircuitBreaker{
		store:   st,
		entries: make(map[int64]*breakerEntry),
	}
}

// SetDefaults injects the live defaults source. nil disables
// the live-source path and the breaker falls back to its built-in
// constants.
func (b *CircuitBreaker) SetDefaults(d BreakerDefaults) {
	b.defaults = d
}

func (b *CircuitBreaker) cfgFor(channelID int64) (int, time.Duration) {
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
	if ch, err := b.store.GetChannel(channelID); err == nil && ch != nil {
		if ch.CircuitBreaker.MaxFailures > 0 {
			maxFail = int64(ch.CircuitBreaker.MaxFailures)
		}
		if ch.CircuitBreaker.ResetTimeout > 0 {
			resetDur = ch.CircuitBreaker.ResetTimeout
		}
	}
	return int(maxFail), resetDur
}

func (b *CircuitBreaker) getEntry(channelID int64) *breakerEntry {
	b.mu.RLock()
	entry, ok := b.entries[channelID]
	b.mu.RUnlock()
	if ok {
		return entry
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if entry, ok = b.entries[channelID]; ok {
		return entry
	}
	entry = &breakerEntry{}
	b.entries[channelID] = entry
	return entry
}

func (b *CircuitBreaker) reload(channelID int64) {
	entry := b.getEntry(channelID)
	entry.mu.Lock()
	entry.failures = 0
	entry.isOpen = false
	entry.mu.Unlock()
}

// reloadAll clears every breaker's state, returning every channel
// to the closed position. Called by admin /reload.
func (b *CircuitBreaker) reloadAll() {
	b.mu.Lock()
	b.entries = make(map[int64]*breakerEntry)
	b.mu.Unlock()
}

func (b *CircuitBreaker) Filter(channels []*model.Channel) []*model.Channel {
	var healthy []*model.Channel
	for _, ch := range channels {
		entry := b.getEntry(ch.ID)
		entry.mu.Lock()
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
	entry.mu.Unlock()
}

func (b *CircuitBreaker) RecordFailure(channelID int64) {
	entry := b.getEntry(channelID)
	maxFail, _ := b.cfgFor(channelID)
	entry.mu.Lock()
	entry.failures++
	entry.lastFailure = time.Now()
	if entry.failures >= maxFail {
		entry.isOpen = true
	}
	entry.mu.Unlock()
}