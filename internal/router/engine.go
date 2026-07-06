package router

import (
	"context"
	"log"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/store"
)

type RouterEngine struct {
	static  *StaticRouter
	breaker *CircuitBreaker
	cost    *CostRouter
	pool    *pool.ChannelPool
	store   store.Store
}

type RouteResult struct {
	Channel   *model.Channel
	Key       *model.Key
	KeyValue  string
	RouterLog string
	TokenID   int64
}

// New constructs the router from the live store, so per-channel
// circuit-breaker config and channels always reflect the latest DB
// state without restarting.
func New(st store.Store, pool *pool.ChannelPool) *RouterEngine {
	return &RouterEngine{
		static:  NewStaticRouter(st),
		breaker: NewCircuitBreaker(st),
		cost:    NewCostRouter(),
		pool:    pool,
		store:   st,
	}
}

// ReloadChannel picks up new circuit-breaker settings after the
// admin updates a channel.
func (e *RouterEngine) ReloadChannel(channelID int64) {
	e.breaker.reload(channelID)
}

func (e *RouterEngine) Route(ctx context.Context, modelName string) (*RouteResult, error) {
	start := time.Now()
	var logParts []string

	candidates := e.static.Match(modelName)
	logParts = append(logParts, "L1(static)")
	if len(candidates) == 0 {
		return nil, ErrNoChannel
	}

	candidates = e.breaker.Filter(candidates)
	logParts = append(logParts, "L2(breaker)")
	if len(candidates) == 0 {
		return nil, ErrAllBroken
	}

	candidates = e.cost.Sort(candidates)
	logParts = append(logParts, "L3(cost)")

	ch := candidates[0]
	logParts = append(logParts, "select="+ch.Name)

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