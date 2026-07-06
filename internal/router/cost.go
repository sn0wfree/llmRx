package router

import (
	"sort"
	"sync"

	"github.com/sn0wfree/llmRx/internal/model"
)

type CostRouter struct {
	mu       sync.RWMutex
	strategy model.CostStrategy
}

func NewCostRouter() *CostRouter {
	return &CostRouter{strategy: model.StrategyCheapest}
}

func (r *CostRouter) Strategy() model.CostStrategy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.strategy
}

// SetStrategy updates the active strategy at runtime. The empty
// string or an unknown value falls back to "cheapest".
func (r *CostRouter) SetStrategy(s model.CostStrategy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch s {
	case model.StrategyCheapest, model.StrategyFastest, model.StrategyBalanced:
		r.strategy = s
	default:
		r.strategy = model.StrategyCheapest
	}
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

	strategy := r.Strategy()

	switch strategy {
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