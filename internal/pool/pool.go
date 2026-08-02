package pool

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

var ErrNoKey = errors.New("no available key")

type ChannelPool struct {
	mu       sync.RWMutex
	channels map[int64]*channelEntry
}

type channelEntry struct {
	Channel *model.Channel
	// Keys is an ordered slice preserved across reloads so
	// NextKey's round-robin counter stays stable when individual
	// keys are added/removed. Each entry's DBID is the SQLite
	// keys.id primary key, which the API logs as logs.key_id.
	Keys    []*keyEntry
	counter uint64
}

type keyEntry struct {
	DBID   int64
	Key    string
	Status model.KeyStatus
}

func NewChannelPool() *ChannelPool {
	return &ChannelPool{channels: make(map[int64]*channelEntry)}
}

// LoadFromStore rebuilds the in-memory channel/keys tables from the
// provided store. Channels that are not Enabled are skipped. Keys
// with status other than KeyActive are still loaded but skipped by
// NextKey. Each keyEntry carries the SQLite primary key so
// NextKey can return a model.Key whose ID matches logs.key_id.
//
// Counter inheritance: when a channel's key set is unchanged
// (same DBIDs, same order) the previous round-robin counter is
// preserved so reloads do not funnel requests onto the first
// key. If the key set differs, the counter resets to 0.
func (p *ChannelPool) LoadFromStore(st store.Store) error {
	chs, err := st.GetChannels()
	if err != nil {
		return err
	}

	// Capture existing counters under read lock so we can inherit
	// them once the new map is built.
	p.mu.RLock()
	prevCounters := make(map[int64]uint64, len(p.channels))
	prevKeys := make(map[int64][]int64, len(p.channels))
	for id, entry := range p.channels {
		prevCounters[id] = atomic.LoadUint64(&entry.counter)
		ids := make([]int64, len(entry.Keys))
		for j, ke := range entry.Keys {
			ids[j] = ke.DBID
		}
		prevKeys[id] = ids
	}
	p.mu.RUnlock()

	next := make(map[int64]*channelEntry, len(chs))
	for i := range chs {
		ch := &chs[i]
		if ch.Status != model.ChannelEnabled {
			continue
		}
		keys, err := st.GetKeys(ch.ID)
		if err != nil {
			return err
		}
		entries := make([]*keyEntry, 0, len(keys))
		dbids := make([]int64, 0, len(keys))
		for j := range keys {
			k := &keys[j]
			entries = append(entries, &keyEntry{DBID: k.ID, Key: k.Key, Status: k.Status})
			dbids = append(dbids, k.ID)
		}
		ce := &channelEntry{Channel: ch, Keys: entries}
		// Inherit counter only if the key DBID set is byte-identical
		// in the same order. Anything else means the channel's key
		// topology changed and a fresh counter is safer.
		if prev, ok := prevKeys[ch.ID]; ok && keyDBIDsEqual(prev, dbids) {
			atomic.StoreUint64(&ce.counter, prevCounters[ch.ID])
		}
		next[ch.ID] = ce
	}

	p.mu.Lock()
	p.channels = next
	p.mu.Unlock()
	return nil
}

// UpsertChannel inserts or refreshes one channel in the in-memory
// pool from a freshly-loaded Channel + Keys slice. Callers should
// update the store first, then call this to avoid races.
//
// Counter inheritance: same logic as LoadFromStore — preserved
// only if the new key DBID set matches the old one in order.
func (p *ChannelPool) UpsertChannel(ch *model.Channel, keys []model.Key) {
	entries := make([]*keyEntry, 0, len(keys))
	dbids := make([]int64, 0, len(keys))
	for i := range keys {
		k := &keys[i]
		entries = append(entries, &keyEntry{DBID: k.ID, Key: k.Key, Status: k.Status})
		dbids = append(dbids, k.ID)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ce := &channelEntry{Channel: ch, Keys: entries}
	if prev, ok := p.channels[ch.ID]; ok {
		prevDBIDs := make([]int64, len(prev.Keys))
		for j, ke := range prev.Keys {
			prevDBIDs[j] = ke.DBID
		}
		if keyDBIDsEqual(prevDBIDs, dbids) {
			atomic.StoreUint64(&ce.counter, atomic.LoadUint64(&prev.counter))
		}
	}
	p.channels[ch.ID] = ce
}

// keyDBIDsEqual reports whether two DBID slices contain the same
// primary keys in the same order. Used to decide whether a reload
// can safely inherit the previous round-robin counter.
func keyDBIDsEqual(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// RemoveChannel drops one channel from the in-memory pool.
func (p *ChannelPool) RemoveChannel(id int64) {
	p.mu.Lock()
	delete(p.channels, id)
	p.mu.Unlock()
}

// NextKey returns the next available key for the given channel
// using a per-channel round-robin counter. The returned model.Key
// carries the SQLite keys.id primary key as ID so audit logs and
// analytics that join on key_id stay in sync with the database
// even after deletes/recreates.
func (p *ChannelPool) NextKey(channelID int64) (*model.Key, error) {
	p.mu.RLock()
	entry, ok := p.channels[channelID]
	p.mu.RUnlock()
	if !ok {
		return nil, ErrNoKey
	}

	n := len(entry.Keys)
	if n == 0 {
		return nil, ErrNoKey
	}

	start := atomic.AddUint64(&entry.counter, 1) - 1
	for i := uint64(0); i < uint64(n); i++ {
		idx := int((start + i) % uint64(n))
		ke := entry.Keys[idx]
		if ke.Status != model.KeyActive {
			continue
		}
		masked := ke.Key
		if len(masked) > 8 {
			masked = masked[:4] + "***" + masked[len(masked)-4:]
		}
		return &model.Key{
			ID:        ke.DBID,
			ChannelID: channelID,
			Key:       ke.Key,
			KeyMasked: masked,
			Status:    ke.Status,
		}, nil
	}
	return nil, ErrNoKey
}

func (p *ChannelPool) GetAllChannels() []*model.Channel {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*model.Channel, 0, len(p.channels))
	for _, entry := range p.channels {
		out = append(out, entry.Channel)
	}
	return out
}
