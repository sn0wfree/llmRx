package router

import (
	"errors"

	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/model"
)

var (
	ErrNoChannel  = errors.New("no channel matched")
	ErrAllBroken  = errors.New("all channels are broken")
	ErrNoKey      = errors.New("no available key")
)

type StaticRouter struct {
	channels []*model.Channel
}

func NewStaticRouter(cfg *config.Config) *StaticRouter {
	channels := make([]*model.Channel, len(cfg.Channels))
	for i, cc := range cfg.Channels {
		channels[i] = &model.Channel{
			ID:        int64(i + 1),
			Name:      cc.Name,
			Provider:  cc.Provider,
			BaseURL:   cc.BaseURL,
			Models:    cc.Models,
			Priority:  cc.Priority,
			InputPrice:  cc.InputPrice,
			OutputPrice: cc.OutputPrice,
			Status:    model.ChannelEnabled,
		}
	}
	return &StaticRouter{channels: channels}
}

func (r *StaticRouter) Match(modelName string) []*model.Channel {
	var candidates []*model.Channel
	for _, ch := range r.channels {
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
	// Sort by priority descending
	for i := 0; i < len(candidates); i++ {
		for j := i + 1; j < len(candidates); j++ {
			if candidates[j].Priority > candidates[i].Priority {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}
	return candidates
}
