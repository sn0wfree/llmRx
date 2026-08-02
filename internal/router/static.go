package router

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

var (
	ErrNoChannel = errors.New("no channel matched")
	ErrAllBroken = errors.New("all channels are broken")
	ErrNoKey     = errors.New("no available key")
)

// StaticRouter performs L1 model-name -> channel lookup. Channels
// are cached in memory and refreshed on demand via Reload(). The
// hot path (Match/MatchAny) is allocation-light and lock-free after
// the cache is populated.
type StaticRouter struct {
	store store.Store

	// channelsSnapshot is a frozen, sorted-by-priority slice of enabled
	// channels read from the store. The atomic pointer lets Match()
	// read the snapshot without a lock; Reload() swaps the pointer
	// under channelsMu.
	channelsSnapshot atomic.Value // holds *[]model.Channel
	channelsMu       sync.Mutex   // serializes Reload() calls
}

func NewStaticRouter(st store.Store) *StaticRouter {
	r := &StaticRouter{store: st}
	r.Reload()
	return r
}

// Reload re-reads the enabled channel list from the store and
// rebuilds the snapshot. Safe to call concurrently; the most recent
// winner is observed by Match()/MatchAny().
//
// Returns the new channel count so callers (e.g. tests) can assert.
func (r *StaticRouter) Reload() int {
	chs, err := r.store.GetChannels()
	if err != nil {
		return 0
	}
	enabled := make([]model.Channel, 0, len(chs))
	for i := range chs {
		ch := chs[i]
		if ch.Status != model.ChannelEnabled {
			continue
		}
		enabled = append(enabled, ch)
	}
	sort.SliceStable(enabled, func(i, j int) bool {
		return enabled[i].Priority > enabled[j].Priority
	})
	snapshot := make([]model.Channel, len(enabled))
	copy(snapshot, enabled)
	r.channelsMu.Lock()
	r.channelsSnapshot.Store(&snapshot)
	r.channelsMu.Unlock()
	return len(snapshot)
}

// snapshot returns the current enabled-channel snapshot. Callers
// receive pointers into the immutable snapshot; the snapshot is
// never mutated after Store(), so concurrent reads are safe.
func (r *StaticRouter) snapshot() []*model.Channel {
	v := r.channelsSnapshot.Load()
	if v == nil {
		return nil
	}
	snap := v.(*[]model.Channel)
	out := make([]*model.Channel, len(*snap))
	for i := range *snap {
		out[i] = &(*snap)[i]
	}
	return out
}

func (r *StaticRouter) Match(modelName string) []*model.Channel {
	snap := r.ensureLoaded()
	if len(snap) == 0 {
		return nil
	}
	var candidates []*model.Channel
	for _, ch := range snap {
		for _, m := range ch.Models {
			if m == modelName {
				candidates = append(candidates, ch)
				break
			}
		}
	}
	return candidates
}

// MatchAny returns enabled channels that serve any of the given
// model names. Used by combo load_balance mode to expand a pool of
// underlying models into a candidate set. The result is sorted by
// descending priority, same as Match.
func (r *StaticRouter) MatchAny(models []string) []*model.Channel {
	snap := r.ensureLoaded()
	if len(snap) == 0 {
		return nil
	}
	modelSet := make(map[string]bool, len(models))
	for _, m := range models {
		modelSet[m] = true
	}
	var candidates []*model.Channel
	for _, ch := range snap {
		for _, m := range ch.Models {
			if modelSet[m] {
				candidates = append(candidates, ch)
				break
			}
		}
	}
	return candidates
}

// ensureLoaded returns the snapshot, lazily calling Reload() on a
// cache miss so callers that constructed the router before any
// channel existed (or before a test seeded one) still see data.
func (r *StaticRouter) ensureLoaded() []*model.Channel {
	if r.channelsSnapshot.Load() == nil {
		r.Reload()
	}
	return r.snapshot()
}
