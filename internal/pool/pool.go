package pool

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/model"
)

var ErrNoKey = errors.New("no available key")

type ChannelPool struct {
	mu       sync.RWMutex
	channels map[int64]*channelEntry
}

type channelEntry struct {
	Channel *model.Channel
	Keys    []*keyEntry
	counter atomic.Uint64
}

type keyEntry struct {
	Key    string
	Status model.KeyStatus
}

func NewChannelPool(cfg *config.Config) *ChannelPool {
	p := &ChannelPool{
		channels: make(map[int64]*channelEntry),
	}

	for i, cc := range cfg.Channels {
		id := int64(i + 1)
		keys := make([]*keyEntry, len(cc.Keys))
		for j, k := range cc.Keys {
			masked := k
			if len(masked) > 8 {
				masked = masked[:4] + "***" + masked[len(masked)-4:]
			}
			keys[j] = &keyEntry{Key: k, Status: model.KeyActive}
		}
		p.channels[id] = &channelEntry{
			Channel: &model.Channel{
				ID:         id,
				Name:       cc.Name,
				Provider:   cc.Provider,
				BaseURL:    cc.BaseURL,
				Models:     cc.Models,
				Priority:   cc.Priority,
				InputPrice: cc.InputPrice,
				OutputPrice: cc.OutputPrice,
				Status:     model.ChannelEnabled,
			},
			Keys: keys,
		}
	}
	return p
}

func (p *ChannelPool) NextKey(channelID int64) (*model.Key, error) {
	p.mu.RLock()
	entry, ok := p.channels[channelID]
	p.mu.RUnlock()
	if !ok {
		return nil, ErrNoKey
	}

	if len(entry.Keys) == 0 {
		return nil, ErrNoKey
	}

	n := entry.counter.Add(1) - 1
	idx := int(n) % len(entry.Keys)
	ke := entry.Keys[idx]

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

func (p *ChannelPool) GetAllChannels() []*model.Channel {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var result []*model.Channel
	for _, entry := range p.channels {
		result = append(result, entry.Channel)
	}
	return result
}
