package router

import (
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/model"
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

type channelCfg struct {
	maxFail  int
	resetDur time.Duration
}

type CircuitBreaker struct {
	mu      sync.RWMutex
	entries map[int64]*breakerEntry
	cfg     map[int64]channelCfg
}

func NewCircuitBreaker(cfg *config.Config) *CircuitBreaker {
	b := &CircuitBreaker{
		entries: make(map[int64]*breakerEntry),
		cfg:     make(map[int64]channelCfg, len(cfg.Channels)),
	}
	for i, cc := range cfg.Channels {
		id := int64(i + 1)
		maxFail := cc.MaxFailures
		if maxFail <= 0 {
			maxFail = defaultMaxFailures
		}
		resetDur := time.Duration(cc.ResetTimeoutMs) * time.Millisecond
		if resetDur <= 0 {
			resetDur = defaultResetDur
		}
		b.cfg[id] = channelCfg{maxFail: maxFail, resetDur: resetDur}
	}
	return b
}

func (b *CircuitBreaker) cfgFor(channelID int64) channelCfg {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if c, ok := b.cfg[channelID]; ok {
		return c
	}
	return channelCfg{maxFail: defaultMaxFailures, resetDur: defaultResetDur}
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

// Filter removes channels that are currently open and not yet past
// the reset timeout. Half-open channels (past timeout) are admitted
// and their failure counter is reset so the next call decides.
func (b *CircuitBreaker) Filter(channels []*model.Channel) []*model.Channel {
	var healthy []*model.Channel
	for _, ch := range channels {
		entry := b.getEntry(ch.ID)
		entry.mu.Lock()
		if entry.isOpen {
			cc := b.cfgFor(ch.ID)
			if time.Since(entry.lastFailure) > cc.resetDur {
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
	cc := b.cfgFor(channelID)
	entry.mu.Lock()
	entry.failures++
	entry.lastFailure = time.Now()
	if entry.failures >= cc.maxFail {
		entry.isOpen = true
	}
	entry.mu.Unlock()
}