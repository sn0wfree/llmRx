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

// LatencyProvider supplies per-channel EWMA latency in ms. The
// latency strategies need this to rank candidates; nil means
// "no latency data" and the strategies fall back to cost order.
type LatencyProvider interface {
	LatencyMS(channelID int64) float64
}

// LatencyStrategy ranks candidates by their EWMA response time
// (ascending - fastest first). Channels with no observations
// (LatencyMS returns 0) stay in their input order at the end of
// the list, so cold-start traffic still flows.
//
// The caller must wire the CostRouter's strategy interface via
// SetLatencyProvider() before invoking Sort; otherwise Sort
// returns the input unchanged.
type LatencyStrategy struct {
	Provider LatencyProvider
}

func (LatencyStrategy) Name() string { return string(model.StrategyLatency) }

// Sort returns the channels sorted by EWMA latency ascending. The
// implementation is stable for channels with equal latency (preserves
// input order, which is normally the cost order from L3).
func (s LatencyStrategy) Sort(channels []*model.Channel) []*model.Channel {
	if len(channels) <= 1 || s.Provider == nil {
		return channels
	}
	type indexed struct {
		ch     *model.Channel
		lat    float64
		hasObs bool
	}
	items := make([]indexed, len(channels))
	for i, ch := range channels {
		lat := s.Provider.LatencyMS(ch.ID)
		items[i] = indexed{ch: ch, lat: lat, hasObs: lat > 0}
	}
	out := make([]*model.Channel, 0, len(channels))
	// Observed channels sorted ascending by latency (stable).
	observed := make([]indexed, 0, len(channels))
	for _, it := range items {
		if it.hasObs {
			observed = append(observed, it)
		}
	}
	sort.SliceStable(observed, func(i, j int) bool {
		return observed[i].lat < observed[j].lat
	})
	for _, it := range observed {
		out = append(out, it.ch)
	}
	// Cold-start channels keep their input order at the end.
	for _, it := range items {
		if !it.hasObs {
			out = append(out, it.ch)
		}
	}
	return out
}

// CostLatencyBlendStrategy blends cost and EWMA latency into a
// single score: score = costWeight*costNorm + latencyWeight*latencyNorm.
// Weights are configurable; both are normalised 0..1 by the worst
// value in the candidate set, so the relative gap matters more
// than the absolute numbers.
//
// Default weights (set via SetCostWeight/SetLatencyWeight) are
// 0.7/0.3 - same shape as LiteLLM's cost_quality_tradeoff.
type CostLatencyBlendStrategy struct {
	Provider     LatencyProvider
	CostWeight   float64 // default 0.7
	LatencyWeight float64 // default 0.3
}

func (CostLatencyBlendStrategy) Name() string { return string(model.StrategyCostLatency) }

// Sort ranks channels by blended score ascending (lower = better).
func (s CostLatencyBlendStrategy) Sort(channels []*model.Channel) []*model.Channel {
	if len(channels) <= 1 {
		return channels
	}
	cw := s.CostWeight
	if cw <= 0 {
		cw = 0.7
	}
	lw := s.LatencyWeight
	if lw <= 0 {
		lw = 0.3
	}
	// Find max for normalisation.
	maxPrice, maxLat := 0.0, 0.0
	for _, ch := range channels {
		if p := totalPrice(ch); p > maxPrice {
			maxPrice = p
		}
		if s.Provider != nil {
			if l := s.Provider.LatencyMS(ch.ID); l > maxLat {
				maxLat = l
			}
		}
	}
	type scored struct {
		ch    *model.Channel
		score float64
	}
	out := make([]scored, len(channels))
	for i, ch := range channels {
		price := totalPrice(ch)
		priceNorm := 0.0
		if maxPrice > 0 {
			priceNorm = price / maxPrice
		}
		lat := 0.0
		if s.Provider != nil {
			lat = s.Provider.LatencyMS(ch.ID)
		}
		latNorm := 0.0
		if maxLat > 0 {
			latNorm = lat / maxLat
		}
		out[i] = scored{ch: ch, score: cw*priceNorm + lw*latNorm}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].score < out[j].score
	})
	result := make([]*model.Channel, len(out))
	for i, s := range out {
		result[i] = s.ch
	}
	return result
}

func strategyFromName(name model.CostStrategy, lp LatencyProvider) CostStrategy {
	switch name {
	case model.StrategyLatency:
		return LatencyStrategy{Provider: lp}
	case model.StrategyCostLatency:
		return CostLatencyBlendStrategy{Provider: lp}
	}
	if s, ok := strategyMap[name]; ok {
		return s
	}
	return CheapestStrategy{}
}

// strategyFromNameWeights is like strategyFromName but also
// carries the configured blend weights for the
// cost_latency_blend strategy. Used by CostRouter.SetStrategy
// so config (latency_routing.cost_weight) reaches the strategy.
func strategyFromNameWeights(name model.CostStrategy, lp LatencyProvider, costW, latW float64) CostStrategy {
	switch name {
	case model.StrategyCostLatency:
		return CostLatencyBlendStrategy{
			Provider:      lp,
			CostWeight:    costW,
			LatencyWeight: latW,
		}
	}
	return strategyFromName(name, lp)
}
