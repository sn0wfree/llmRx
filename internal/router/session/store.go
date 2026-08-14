// Package session implements session affinity for the auto router:
// multi-turn conversations are pinned to the model+channel that
// served the first turn, preserving upstream prompt caches
// (Anthropic cache_control, OpenAI GPT-5+ prompt cache) and giving
// multi-turn consistency.
//
// A pin is keyed by a session ID and expires after a TTL. On the
// next request for the same session, the router skips
// classification and arm sampling and routes straight to the
// pinned model. The pin also records the complexity tier so the
// decision log stays consistent.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// Pin is one session's routing decision: the model (and tier) that
// served the first turn of the conversation, valid until ExpiresAt.
type Pin struct {
	Model     string    `json:"model"`
	Tier      string    `json:"tier"`
	ChannelID int64     `json:"channel_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Store is a concurrent map of session ID -> Pin with TTL expiry
// and a bounded entry count. Eviction of expired pins happens
// lazily on access plus a periodic sweep started by StartCleanup.
type Store struct {
	mu   sync.RWMutex
	pins map[string]Pin
	ttl  time.Duration
	maxN int

	stop chan struct{}
	once sync.Once
}

// Config carries construction parameters.
type Config struct {
	// TTL bounds how long a pin stays valid after creation.
	// Default 1h.
	TTL time.Duration
	// MaxEntries caps the pin table. When full, new sessions
	// evict the oldest-expiring pin. Default 10000.
	MaxEntries int
}

// New returns an empty store. Callers must call StartCleanup (or
// Stop) to reclaim expired pins on a schedule.
func New(cfg Config) *Store {
	if cfg.TTL <= 0 {
		cfg.TTL = time.Hour
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = 10000
	}
	return &Store{
		pins: make(map[string]Pin),
		ttl:  cfg.TTL,
		maxN: cfg.MaxEntries,
		stop: make(chan struct{}),
	}
}

// StartCleanup launches the periodic expired-pin sweep. Safe to
// call multiple times; the underlying goroutine runs once.
func (s *Store) StartCleanup(interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	s.once.Do(func() {
		go func() {
			t := time.NewTicker(interval)
			defer t.Stop()
			for {
				select {
				case <-s.stop:
					return
				case <-t.C:
					s.Sweep()
				}
			}
		}()
	})
}

// Stop terminates the cleanup goroutine. Safe to call multiple
// times; pins themselves are not cleared.
func (s *Store) Stop() {
	s.once.Do(func() { close(s.stop) })
}

// Get returns the pin for sessionID if it exists and is not
// expired. Expired pins are deleted opportunistically.
func (s *Store) Get(sessionID string) (Pin, bool) {
	if sessionID == "" {
		return Pin{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pins[sessionID]
	if !ok {
		return Pin{}, false
	}
	if time.Now().After(p.ExpiresAt) {
		delete(s.pins, sessionID)
		return Pin{}, false
	}
	return p, true
}

// Set stores (or refreshes) the pin for sessionID. When the table
// is at capacity the entry expiring soonest is evicted first.
func (s *Store) Set(sessionID string, model, tier string, channelID int64) {
	if sessionID == "" {
		return
	}
	now := time.Now()
	p := Pin{
		Model:     model,
		Tier:      tier,
		ChannelID: channelID,
		ExpiresAt: now.Add(s.ttl),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pins[sessionID]; !ok && len(s.pins) >= s.maxN {
		s.evictOldestLocked()
	}
	s.pins[sessionID] = p
}

// Delete removes the pin for sessionID (e.g. after the channel is
// known unhealthy).
func (s *Store) Delete(sessionID string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pins, sessionID)
}

// Sweep removes every expired pin. Called periodically by the
// cleanup goroutine and directly by tests.
func (s *Store) Sweep() {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, p := range s.pins {
		if now.After(p.ExpiresAt) {
			delete(s.pins, id)
		}
	}
}

// Count returns the number of live pins.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.pins)
}

// Snapshot returns a copy of all live pins (admin state endpoint).
func (s *Store) Snapshot() map[string]Pin {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Pin, len(s.pins))
	for k, v := range s.pins {
		out[k] = v
	}
	return out
}

// Reset clears every pin. Called from admin /reload.
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pins = make(map[string]Pin)
}

// evictOldestLocked drops the pin expiring soonest. Callers must
// hold s.mu.
func (s *Store) evictOldestLocked() {
	var oldestID string
	var oldestAt time.Time
	first := true
	for id, p := range s.pins {
		if first || p.ExpiresAt.Before(oldestAt) {
			oldestID = id
			oldestAt = p.ExpiresAt
			first = false
		}
	}
	if oldestID != "" {
		delete(s.pins, oldestID)
	}
}

// Hash derives a stable session ID from the system prompt + first
// user message, so clients that don't send an explicit session_id
// still get affinity within one conversation. The hash is a
// 32-char hex digest (no content stored).
func Hash(system, firstUser string) string {
	h := sha256.New()
	h.Write([]byte(system))
	h.Write([]byte{0})
	h.Write([]byte(firstUser))
	return hex.EncodeToString(h.Sum(nil))
}
