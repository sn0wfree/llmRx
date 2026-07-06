package router

import (
	"context"
	"log"
	"time"

	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
)

type RouterEngine struct {
	static  *StaticRouter
	breaker *CircuitBreaker
	cost    *CostRouter
	pool    *pool.ChannelPool
}

type RouteResult struct {
	Channel   *model.Channel
	Key       *model.Key
	KeyValue  string
	RouterLog string
}

func New(cfg *config.Config, pool *pool.ChannelPool) *RouterEngine {
	return &RouterEngine{
		static:  NewStaticRouter(cfg),
		breaker: NewCircuitBreaker(cfg),
		cost:    NewCostRouter(),
		pool:    pool,
	}
}

func (e *RouterEngine) Route(ctx context.Context, modelName string) (*RouteResult, error) {
	start := time.Now()
	var logParts []string

	// L1: Static routing — match model to channels
	candidates := e.static.Match(modelName)
	logParts = append(logParts, "L1(static)")
	if len(candidates) == 0 {
		return nil, ErrNoChannel
	}

	// L2: Circuit breaker — filter out broken channels
	candidates = e.breaker.Filter(candidates)
	logParts = append(logParts, "L2(breaker)")
	if len(candidates) == 0 {
		return nil, ErrAllBroken
	}

	// L3: Cost optimization — sort by price
	candidates = e.cost.Sort(candidates)
	logParts = append(logParts, "L3(cost)")

	// Select the best channel
	ch := candidates[0]
	logParts = append(logParts, "select="+ch.Name)

	// Get an API key from the pool
	key, err := e.pool.NextKey(ch.ID)
	if err != nil {
		return nil, err
	}

	result := &RouteResult{
		Channel:   ch,
		Key:       key,
		KeyValue:  key.Key,
		RouterLog: joinLog(logParts),
	}

	log.Printf("[router] %s → %s (%v)", modelName, ch.Name, time.Since(start))
	return result, nil
}

func (e *RouterEngine) RecordSuccess(channelID int64) {
	e.breaker.RecordSuccess(channelID)
}

func (e *RouterEngine) RecordFailure(channelID int64) {
	e.breaker.RecordFailure(channelID)
}

func joinLog(parts []string) string {
	s := ""
	for i, p := range parts {
		if i > 0 {
			s += " → "
		}
		s += p
	}
	return s
}
