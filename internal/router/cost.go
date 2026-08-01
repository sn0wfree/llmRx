package router

import (
	"sync"

	"github.com/sn0wfree/llmRx/internal/model"
)

type CostRouter struct {
	mu       sync.RWMutex
	strategy CostStrategy
}

func NewCostRouter() *CostRouter {
	return &CostRouter{strategy: CheapestStrategy{}}
}

func (r *CostRouter) Strategy() model.CostStrategy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return model.CostStrategy(r.strategy.Name())
}

func (r *CostRouter) StrategyInterface() CostStrategy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.strategy
}

func (r *CostRouter) SetStrategy(s model.CostStrategy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.strategy = strategyFromName(s)
}

func (r *CostRouter) SetStrategyInterface(s CostStrategy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.strategy = s
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