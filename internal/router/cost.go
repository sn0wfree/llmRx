package router

import (
	"sync"
	"sync/atomic"

	"github.com/sn0wfree/llmRx/internal/model"
)

// strategyHolder wraps a CostStrategy so atomic.Value always sees
// the same concrete type (*strategyHolder), avoiding the "store of
// inconsistently typed value" panic when the strategy implementation
// changes (e.g. CheapestStrategy → FastestStrategy).
type strategyHolder struct {
	s CostStrategy
}

type CostRouter struct {
	strategy atomic.Value // holds *strategyHolder
	// latencyProvider supplies per-channel EWMA latency for the
	// latency-aware strategies (latency, cost_latency_blend). nil
	// means "no latency data" - those strategies fall back to
	// cost order.
	latencyProvider LatencyProvider
	// blendWeights are the cost/latency weights for the
	// cost_latency_blend strategy. Zero values fall back to the
	// defaults (0.7 / 0.3) in the strategy itself.
	blendCost     float64
	blendLatency  float64
	blendMu       sync.RWMutex
}

// NewCostRouter returns a router with cheapest as the default strategy.
func NewCostRouter() *CostRouter {
	r := &CostRouter{}
	r.strategy.Store(&strategyHolder{s: CheapestStrategy{}})
	return r
}

// SetBlendWeights overrides the cost/latency weights used by the
// cost_latency_blend strategy. Zero values keep the strategy
// defaults. Takes effect on the next Sort() call.
func (r *CostRouter) SetBlendWeights(cost, latency float64) {
	r.blendMu.Lock()
	r.blendCost = cost
	r.blendLatency = latency
	r.blendMu.Unlock()
}

// blendWeightsSnapshot returns the current configured weights.
func (r *CostRouter) blendWeightsSnapshot() (float64, float64) {
	r.blendMu.RLock()
	defer r.blendMu.RUnlock()
	return r.blendCost, r.blendLatency
}

// SetLatencyProvider injects the source of per-channel latency.
// Called once at startup when the latency tracker is constructed.
func (r *CostRouter) SetLatencyProvider(p LatencyProvider) {
	r.latencyProvider = p
	r.refreshStrategy()
}

func (r *CostRouter) Strategy() model.CostStrategy {
	return model.CostStrategy(r.strategy.Load().(*strategyHolder).s.Name())
}

func (r *CostRouter) StrategyInterface() CostStrategy {
	return r.strategy.Load().(*strategyHolder).s
}

func (r *CostRouter) SetStrategy(s model.CostStrategy) {
	cw, lw := r.blendWeightsSnapshot()
	r.strategy.Store(&strategyHolder{s: strategyFromNameWeights(s, r.latencyProvider, cw, lw)})
}

func (r *CostRouter) SetStrategyInterface(s CostStrategy) {
	r.strategy.Store(&strategyHolder{s: s})
}

// refreshStrategy re-installs the current strategy name so a newly
// wired LatencyProvider (or blend weights) takes effect on the next
// Sort() call.
func (r *CostRouter) refreshStrategy() {
	cur := r.Strategy()
	r.SetStrategy(cur)
}

func totalPrice(ch *model.Channel) float64 {
	return ch.InputPrice + ch.OutputPrice
}

func (r *CostRouter) Sort(channels []*model.Channel) []*model.Channel {
	return r.SortWith(channels, "")
}

func (r *CostRouter) SortWith(channels []*model.Channel, strategy model.CostStrategy) []*model.Channel {
	if len(channels) <= 1 {
		return channels
	}

	var s CostStrategy
	if strategy == "" {
		s = r.StrategyInterface()
	} else {
		cw, lw := r.blendWeightsSnapshot()
		s = strategyFromNameWeights(strategy, r.latencyProvider, cw, lw)
	}
	return s.Sort(channels)
}
