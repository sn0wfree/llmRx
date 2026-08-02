package router

import (
	"math/rand"
	"sort"
	"time"

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

// WeightedRandomStrategy spreads traffic across the candidate
// channels with probability inversely proportional to the square
// of the price — the OpenRouter auto-beta shape: channels priced
// 1:2:3 get ~9:4:1 of the traffic. Unlike a pure cheapest sort
// this keeps every channel warm (fewer cold-start stamps) while
// still biasing strongly toward cheap endpoints. Zero-price
// channels (unpriced configs) get weight 1 and therefore rank
// first — treat the strategy as "cheapest-first with noise".
type WeightedRandomStrategy struct{}

func (WeightedRandomStrategy) Name() string { return string(model.StrategyWeightedRandom) }

// Sort returns a weighted random permutation: the first element is
// a weighted draw, the second a weighted draw over the remainder,
// etc. (sampling without replacement), so callers that only read
// channels[0] get exactly one weighted-random pick.
func (WeightedRandomStrategy) Sort(channels []*model.Channel) []*model.Channel {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	weights := make([]float64, len(channels))
	for i, ch := range channels {
		p := totalPrice(ch)
		if p <= 0 {
			weights[i] = 1
			continue
		}
		weights[i] = 1 / (p * p)
	}
	// Sample without replacement.
	out := make([]*model.Channel, 0, len(channels))
	idx := make([]int, len(channels))
	for i := range idx {
		idx[i] = i
	}
	for len(idx) > 0 {
		total := 0.0
		for _, i := range idx {
			total += weights[i]
		}
		draw := rng.Float64() * total
		pick := 0
		acc := 0.0
		for k, i := range idx {
			acc += weights[i]
			if draw <= acc {
				pick = k
				break
			}
		}
		out = append(out, channels[idx[pick]])
		idx = append(idx[:pick], idx[pick+1:]...)
	}
	return out
}

var strategyMap = map[model.CostStrategy]CostStrategy{
	model.StrategyCheapest:       CheapestStrategy{},
	model.StrategyFastest:        FastestStrategy{},
	model.StrategyBalanced:       BalancedStrategy{},
	model.StrategyWeightedRandom: WeightedRandomStrategy{},
}

func strategyFromName(name model.CostStrategy) CostStrategy {
	if s, ok := strategyMap[name]; ok {
		return s
	}
	return CheapestStrategy{}
}
