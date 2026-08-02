package router

import (
	"sort"

	"github.com/sn0wfree/llmRx/internal/model"
)

type CostStrategy interface {
	Name() string
	Sort(channels []*model.Channel) []*model.Channel
}

type CheapestStrategy struct{}

func (CheapestStrategy) Name() string { return "cheapest" }
func (CheapestStrategy) Sort(channels []*model.Channel) []*model.Channel {
	sorted := make([]*model.Channel, len(channels))
	copy(sorted, channels)
	sort.SliceStable(sorted, func(i, j int) bool {
		return totalPrice(sorted[i]) < totalPrice(sorted[j])
	})
	return sorted
}

type FastestStrategy struct{}

func (FastestStrategy) Name() string { return "fastest" }
func (FastestStrategy) Sort(channels []*model.Channel) []*model.Channel {
	sorted := make([]*model.Channel, len(channels))
	copy(sorted, channels)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Priority > sorted[j].Priority
	})
	return sorted
}

type BalancedStrategy struct{}

func (BalancedStrategy) Name() string { return "balanced" }
func (BalancedStrategy) Sort(channels []*model.Channel) []*model.Channel {
	sorted := make([]*model.Channel, len(channels))
	copy(sorted, channels)

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
	return sorted
}

var strategyMap = map[model.CostStrategy]CostStrategy{
	model.StrategyCheapest: CheapestStrategy{},
	model.StrategyFastest:  FastestStrategy{},
	model.StrategyBalanced: BalancedStrategy{},
}

func strategyFromName(name model.CostStrategy) CostStrategy {
	if s, ok := strategyMap[name]; ok {
		return s
	}
	return CheapestStrategy{}
}
