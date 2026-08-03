package router

import (
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
}

func NewCostRouter() *CostRouter {
	r := &CostRouter{}
	r.strategy.Store(&strategyHolder{s: CheapestStrategy{}})
	return r
}

func (r *CostRouter) Strategy() model.CostStrategy {
	return model.CostStrategy(r.strategy.Load().(*strategyHolder).s.Name())
}

func (r *CostRouter) StrategyInterface() CostStrategy {
	return r.strategy.Load().(*strategyHolder).s
}

func (r *CostRouter) SetStrategy(s model.CostStrategy) {
	r.strategy.Store(&strategyHolder{s: strategyFromName(s)})
}

func (r *CostRouter) SetStrategyInterface(s CostStrategy) {
	r.strategy.Store(&strategyHolder{s: s})
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
		s = strategyFromName(strategy)
	}
	return s.Sort(channels)
}
