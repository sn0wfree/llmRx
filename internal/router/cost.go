package router

import (
	"sort"

	"github.com/sn0wfree/llmRx/internal/model"
)

type CostRouter struct {
	strategy model.CostStrategy
}

func NewCostRouter() *CostRouter {
	return &CostRouter{strategy: model.StrategyCheapest}
}

func (r *CostRouter) Sort(channels []*model.Channel) []*model.Channel {
	if len(channels) <= 1 {
		return channels
	}
	sorted := make([]*model.Channel, len(channels))
	copy(sorted, channels)

	switch r.strategy {
	case model.StrategyCheapest:
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].InputPrice+sorted[i].OutputPrice <
				sorted[j].InputPrice+sorted[j].OutputPrice
		})
	case model.StrategyFastest:
		// priority-as-proxy for latency: higher priority first
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].Priority > sorted[j].Priority
		})
	default:
		// balanced: score = price * 0.5 + priority_inv * 0.5
		maxPrice := 0.0
		for _, ch := range sorted {
			p := ch.InputPrice + ch.OutputPrice
			if p > maxPrice {
				maxPrice = p
			}
		}
		sort.Slice(sorted, func(i, j int) bool {
			pi := sorted[i].InputPrice + sorted[i].OutputPrice
			pj := sorted[j].InputPrice + sorted[j].OutputPrice
			si := (pi/maxPrice)*0.5 + float64(sorted[j].Priority)/float64(sorted[i].Priority+1)*0.5
			sj := (pj/maxPrice)*0.5 + float64(sorted[i].Priority)/float64(sorted[j].Priority+1)*0.5
			return si < sj
		})
	}

	return sorted
}
