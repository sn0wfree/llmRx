package router

import (
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

type breakerEntry struct {
	failures     int
	lastFailure  time.Time
	isOpen       bool
	mu           sync.Mutex
}

type CircuitBreaker struct {
	mu       sync.RWMutex
	entries  map[int64]*breakerEntry
	maxFail  int
	resetDur time.Duration
}

func NewCircuitBreaker() *CircuitBreaker {
	return &CircuitBreaker{
		entries:  make(map[int64]*breakerEntry),
		maxFail:  5,
		resetDur: 60 * time.Second,
	}
}

func (b *CircuitBreaker) Filter(channels []*model.Channel) []*model.Channel {
	var healthy []*model.Channel
	for _, ch := range channels {
		b.mu.RLock()
		entry, ok := b.entries[ch.ID]
		b.mu.RUnlock()

		if ok && entry.isOpen {
			entry.mu.Lock()
			if time.Since(entry.lastFailure) > b.resetDur {
				entry.isOpen = false
				entry.failures = 0
				healthy = append(healthy, ch)
			}
			entry.mu.Unlock()
			continue
		}
		healthy = append(healthy, ch)
	}
	return healthy
}

func (b *CircuitBreaker) RecordSuccess(channelID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if entry, ok := b.entries[channelID]; ok {
		entry.mu.Lock()
		entry.failures = 0
		entry.isOpen = false
		entry.mu.Unlock()
	}
}

func (b *CircuitBreaker) RecordFailure(channelID int64) {
	b.mu.Lock()
	entry, ok := b.entries[channelID]
	if !ok {
		entry = &breakerEntry{}
		b.entries[channelID] = entry
	}
	b.mu.Unlock()

	entry.mu.Lock()
	entry.failures++
	entry.lastFailure = time.Now()
	if entry.failures >= b.maxFail {
		entry.isOpen = true
	}
	entry.mu.Unlock()
}
