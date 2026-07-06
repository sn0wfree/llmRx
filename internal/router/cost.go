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

func totalPrice(ch *model.Channel) float64 {
	return ch.InputPrice + ch.OutputPrice
}

// Sort orders channels per the configured cost strategy. The returned
// slice is a copy, so the input is not mutated.
func (r *CostRouter) Sort(channels []*model.Channel) []*model.Channel {
	if len(channels) <= 1 {
		return channels
	}
	sorted := make([]*model.Channel, len(channels))
	copy(sorted, channels)

	switch r.strategy {
	case model.StrategyCheapest:
		sort.SliceStable(sorted, func(i, j int) bool {
			return totalPrice(sorted[i]) < totalPrice(sorted[j])
		})
	case model.StrategyFastest:
		// Priority is treated as a proxy for latency / SLA: higher wins.
		sort.SliceStable(sorted, func(i, j int) bool {
			return sorted[i].Priority > sorted[j].Priority
		})
	default:
		// Balanced: min-max normalize price (lower is better) and
		// priority (higher is better), then weighted sum.
		maxPrice := 0.0
		maxPrio := 0
		for _, ch := range sorted {
			if p := totalPrice(ch); p > maxPrice {
				maxPrice = p
			}
			if ch.Priority > maxPrio {
				maxPrio = ch.Priority
			}
		}
		sort.SliceStable(sorted, func(i, j int) bool {
			pi, pj := totalPrice(sorted[i]), totalPrice(sorted[j])
			pri, prj := sorted[i].Priority, sorted[j].Priority
			priceNorm := func(p float64) float64 {
				if maxPrice == 0 {
					return 0
				}
				return p / maxPrice
			}
			prioNorm := func(p int) float64 {
				if maxPrio == 0 {
					return 0
				}
				return float64(p) / float64(maxPrio)
			}
			scoreI := priceNorm(pi)*0.5 + (1-prioNorm(pri))*0.5
			scoreJ := priceNorm(pj)*0.5 + (1-prioNorm(prj))*0.5
			return scoreI < scoreJ
		})
	}

	return sorted
}