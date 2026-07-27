package router

import (
	"errors"
	"sort"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

var (
	ErrNoChannel = errors.New("no channel matched")
	ErrAllBroken = errors.New("all channels are broken")
	ErrNoKey     = errors.New("no available key")
)

type StaticRouter struct {
	store store.Store
}

func NewStaticRouter(st store.Store) *StaticRouter {
	return &StaticRouter{store: st}
}

func (r *StaticRouter) Match(modelName string) []*model.Channel {
	chs, err := r.store.GetChannels()
	if err != nil {
		return nil
	}
	var candidates []*model.Channel
	for i := range chs {
		ch := &chs[i]
		if ch.Status != model.ChannelEnabled {
			continue
		}
		for _, m := range ch.Models {
			if m == modelName {
				candidates = append(candidates, ch)
				break
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Priority > candidates[j].Priority
	})
	return candidates
}

// MatchAny returns enabled channels that serve any of the given
// model names. Used by combo load_balance mode to expand a pool of
// underlying models into a candidate set. The result is sorted by
// descending priority, same as Match.
func (r *StaticRouter) MatchAny(models []string) []*model.Channel {
	chs, err := r.store.GetChannels()
	if err != nil {
		return nil
	}
	modelSet := make(map[string]bool, len(models))
	for _, m := range models {
		modelSet[m] = true
	}
	var candidates []*model.Channel
	for i := range chs {
		ch := &chs[i]
		if ch.Status != model.ChannelEnabled {
			continue
		}
		for _, m := range ch.Models {
			if modelSet[m] {
				candidates = append(candidates, ch)
				break
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Priority > candidates[j].Priority
	})
	return candidates
}