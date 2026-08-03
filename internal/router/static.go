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

// channelSnapshot is the immutable cache that Match/MatchAny read
// lock-free. Contains both the raw channel slice and a pre-built
// model-name → channel index for O(1) L1 lookups.
type channelSnapshot struct {
	channels []model.Channel
	byModel  map[string][]*model.Channel
}

// StaticRouter performs L1 model-name -> channel lookup. Channels
// are cached in memory and refreshed on demand via Reload(). The
// hot path (Match/MatchAny) is a single atomic load + map lookup.
type StaticRouter struct {
	store store.Store

	// channelsSnapshot holds a *channelSnapshot. The atomic pointer
	// lets Match() read the index without a lock; Reload() builds
	// a new snapshot and swaps the pointer under channelsMu.
	channelsSnapshot atomic.Value // holds *channelSnapshot
	channelsMu       sync.Mutex   // serializes Reload() calls
}

func NewStaticRouter(st store.Store) *StaticRouter {
	r := &StaticRouter{store: st}
	r.Reload()
	return r
}

// Reload re-reads the enabled channel list from the store and
// rebuilds the snapshot + model index. Safe to call concurrently;
// the most recent winner is observed by Match()/MatchAny().
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

	// Build the model-name → channel index.
	byModel := make(map[string][]*model.Channel, len(snapshot))
	for i := range snapshot {
		ch := &snapshot[i]
		for _, m := range ch.Models {
			byModel[m] = append(byModel[m], ch)
		}
	}

	r.channelsMu.Lock()
	r.channelsSnapshot.Store(&channelSnapshot{channels: snapshot, byModel: byModel})
	r.channelsMu.Unlock()
	return len(snapshot)
}

// snapshot returns the current channel snapshot. Callers
// receive the pre-built model index and channel pointers into
// the immutable snapshot.
func (r *StaticRouter) snapshot() *channelSnapshot {
	v := r.channelsSnapshot.Load()
	if v == nil {
		return nil
	}
	return v.(*channelSnapshot)
}

func (r *StaticRouter) Match(modelName string) []*model.Channel {
	snap := r.ensureLoaded()
	if snap == nil {
		return nil
	}
	return snap.byModel[modelName]
}

// MatchAny returns enabled channels that serve any of the given
// model names. Uses the pre-built index for O(1) per-model lookup,
// deduplicating by channel pointer. The result is sorted by
// descending priority (same as the index order).
func (r *StaticRouter) MatchAny(models []string) []*model.Channel {
	snap := r.ensureLoaded()
	if snap == nil || len(models) == 0 {
		return nil
	}
	seen := make(map[*model.Channel]struct{}, len(models))
	var candidates []*model.Channel
	for _, m := range models {
		for _, ch := range snap.byModel[m] {
			if _, ok := seen[ch]; ok {
				continue
			}
			seen[ch] = struct{}{}
			candidates = append(candidates, ch)
		}
	}
	return candidates
}

// ensureLoaded returns the channel snapshot, lazily calling
// Reload() on a cache miss so callers that constructed the router
// before any channel existed still see data.
func (r *StaticRouter) ensureLoaded() *channelSnapshot {
	if r.channelsSnapshot.Load() == nil {
		r.Reload()
	}
	return r.snapshot()
}
