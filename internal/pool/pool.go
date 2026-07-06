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
	Keys    []*keyEntry
	counter uint64
}

type keyEntry struct {
	Key    string
	Status model.KeyStatus
}

func NewChannelPool() *ChannelPool {
	return &ChannelPool{channels: make(map[int64]*channelEntry)}
}

// LoadFromStore rebuilds the in-memory channel/keys tables from the
// provided store. Channels that are not Enabled are skipped. Keys
// with status other than KeyActive are still loaded but skipped by
// NextKey.
func (p *ChannelPool) LoadFromStore(st store.Store) error {
	chs, err := st.GetChannels()
	if err != nil {
		return err
	}
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
		for j := range keys {
			k := &keys[j]
			entries = append(entries, &keyEntry{Key: k.Key, Status: k.Status})
		}
		next[ch.ID] = &channelEntry{Channel: ch, Keys: entries}
	}

	p.mu.Lock()
	p.channels = next
	p.mu.Unlock()
	return nil
}

// UpsertChannel inserts or refreshes one channel in the in-memory
// pool from a freshly-loaded Channel + Keys slice. Callers should
// update the store first, then call this to avoid races.
func (p *ChannelPool) UpsertChannel(ch *model.Channel, keys []model.Key) {
	entries := make([]*keyEntry, 0, len(keys))
	for i := range keys {
		k := &keys[i]
		entries = append(entries, &keyEntry{Key: k.Key, Status: k.Status})
	}
	p.mu.Lock()
	p.channels[ch.ID] = &channelEntry{Channel: ch, Keys: entries}
	p.mu.Unlock()
}

// RemoveChannel drops one channel from the in-memory pool.
func (p *ChannelPool) RemoveChannel(id int64) {
	p.mu.Lock()
	delete(p.channels, id)
	p.mu.Unlock()
}

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
			ID:        int64(idx + 1),
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