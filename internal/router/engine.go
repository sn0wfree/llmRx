package router

import (
	"context"
	"sync/atomic"

	"github.com/sn0wfree/llmRx/internal/intent"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/router/latency"
	"github.com/sn0wfree/llmRx/internal/router/thompson"
	"github.com/sn0wfree/llmRx/internal/store"
)

// RouterEngine is the L1-L5 routing pipeline. Optional helpers
// (circuit breaker, Thompson sampler, intent classifier) hang off
// it; the prober plugin (prober.Cache implements TrafficObserver)
// is also hung off here so every successful/failed real call is
// observed once at the source instead of scattered across api
// handlers.
type RouterEngine struct {
	static        *StaticRouter
	breaker       *CircuitBreaker
	cost          *CostRouter
	thompson      *thompson.Sampler
	intent        intent.Classifier
	pool          *pool.ChannelPool
	store         store.Store
	extraChannels []func() []*model.Channel // BYOK hook (Phase 1.5 reserved)
	trafficObs    atomic.Value               // holds []TrafficObserver; immutable once stored
	latency       *latency.Tracker           // per-channel EWMA latency tracker
}

// TrafficObserver is the pluggable hook RouterEngine invokes on
// every successful or failed real (non-probe) call. Implementations
// must be safe for concurrent use.
type TrafficObserver interface {
	OnRealSuccess(channelID int64)
	OnRealFailure(channelID int64)
}

// SetTrafficObserver replaces all registered observers with a single
// one. Pass nil to clear all observers.
func (e *RouterEngine) SetTrafficObserver(o TrafficObserver) {
	if o == nil {
		e.trafficObs.Store([]TrafficObserver(nil))
	} else {
		e.trafficObs.Store([]TrafficObserver{o})
	}
}

// RegisterTrafficObserver appends an observer. Multiple observers
// each receive all notifications. Safe to call before engine start.
func (e *RouterEngine) RegisterTrafficObserver(o TrafficObserver) {
	if o == nil {
		return
	}
	for {
		old := e.getTrafficObs()
		var updated []TrafficObserver
		if old != nil {
			updated = make([]TrafficObserver, len(old)+1)
			copy(updated, old)
		} else {
			updated = make([]TrafficObserver, 1)
		}
		updated[len(updated)-1] = o
		if e.trafficObs.CompareAndSwap(old, updated) {
			return
		}
	}
}

func (e *RouterEngine) getTrafficObs() []TrafficObserver {
	v := e.trafficObs.Load()
	if v == nil {
		return nil
	}
	return v.([]TrafficObserver)
}

type RouteResult struct {
	Channel   *model.Channel
	Key       *model.Key
	KeyValue  string
	RouterLog string
	TokenID   int64
	Intent    intent.Intent
}

// New constructs the router from the live store, so per-channel
// circuit-breaker config and channels always reflect the latest DB
// state without restarting.
func New(st store.Store, pool *pool.ChannelPool) *RouterEngine {
	latencyTr := latency.New(latency.DefaultAlpha)
	r := &RouterEngine{
		static:   NewStaticRouter(st),
		breaker:  NewCircuitBreaker(st),
		cost:     NewCostRouter(),
		thompson: thompson.New(thompson.Config{}),
		intent:   intent.Nop{},
		pool:     pool,
		store:    st,
		latency:  latencyTr,
	}
	// Wire latency tracker into the cost router so latency-aware
	// strategies can rank candidates by EWMA response time.
	r.cost.SetLatencyProvider(latencyTr)
	return r
}

// SetIntentClassifier injects an L4 classifier. Pass intent.Nop{}
// to disable L4. Safe to call concurrently with Route.
func (e *RouterEngine) SetIntentClassifier(c intent.Classifier) {
	if c == nil {
		c = intent.Nop{}
	}
	e.intent = c
}

// SetBreakerDefaults wires the runtime.Defaults snapshot into
// the circuit breaker so admin /runtime config writes take
// effect on the next request instead of needing a restart.
// nil disables the live-source path.
func (e *RouterEngine) SetBreakerDefaults(d BreakerDefaults) {
	e.breaker.SetDefaults(d)
}

// SetHealthChecker injects an active-probe source (prober.Cache)
// into the circuit breaker. When set, Filter() excludes channels
// that a fresh probe has marked unhealthy. nil disables the
// probe filter.
func (e *RouterEngine) SetHealthChecker(hc HealthChecker) {
	e.breaker.SetHealthChecker(hc)
}

// IntentBackend returns the active classifier's backend name
// (e.g. "disabled" for Nop, "rust" for the native .so). Used by
// the /health endpoint so operators can tell whether L4 is live
// without parsing the bootstrap log.
func (e *RouterEngine) IntentBackend() string {
	if e.intent == nil {
		return "disabled"
	}
	return e.intent.Backend()
}

// ReloadChannel picks up new circuit-breaker settings after the
// admin updates a channel. Also refreshes the static L1 channel
// snapshot so status changes (enable/disable) take effect.
func (e *RouterEngine) ReloadChannel(channelID int64) {
	e.breaker.reload(channelID)
	e.thompson.Reset(channelID)
	e.static.Reload()
}

// ReloadAllChannels walks every known channel ID and clears the
// breaker + Thompson posterior state. Called by admin /reload so
// the routing layer drops state from channels that may have been
// disabled / re-enabled outside the admin path.
func (e *RouterEngine) ReloadAllChannels() {
	e.breaker.reloadAll()
	e.thompson.ResetAll()
	e.static.Reload()
	if e.latency != nil {
		e.latency.Reset()
	}
}

// SetStrategy swaps the cost router's strategy at runtime. The
// change is picked up by the next Route() call.
func (e *RouterEngine) SetStrategy(s model.CostStrategy) {
	e.cost.SetStrategy(s)
}

// LoadThompsonState hydrates L5 from a previously-saved state
// file. Missing file is a no-op (first run). Wraps thompson.Load
// so callers don't need to know the package.
func (e *RouterEngine) LoadThompsonState(path string) error {
	return e.thompson.Load(path)
}

// SaveThompsonState writes the current L5 posteriors to path so
// a graceful shutdown preserves L5's learned weights.
func (e *RouterEngine) SaveThompsonState(path string) error {
	return e.thompson.Save(path)
}

// RegisterExtraChannels installs a callback that returns additional
// channels to consider during L1. Used by the (Phase 1.5 reserved)
// BYOK path to inject consumer-supplied upstream keys into the
// routing pool without writing them to the main channels table.
// nil callbacks are ignored. Safe to call before engine start.
func (e *RouterEngine) RegisterExtraChannels(src func() []*model.Channel) {
	if src == nil {
		return
	}
	e.extraChannels = append(e.extraChannels, src)
}

// CostStrategy returns the currently active strategy.
func (e *RouterEngine) CostStrategy() model.CostStrategy {
	return e.cost.Strategy()
}

// RouteOptions carries per-request context that affects L4 (intent)
// and the log line. It is optional; zero value gives the legacy
// behaviour where no L4 step runs.
type RouteOptions struct {
	Text         string             // last user message, used for L4 intent classification
	ModelSet     []string           // optional L1 override: match any of these models (combo load_balance)
	CostStrategy model.CostStrategy // optional L3 override: "" = use global
	// SkipBreaker bypasses the L2 circuit-breaker filter. Used by
	// the auto router's fallback list (the safety net): a degraded
	// attempt is preferable to failing the request outright.
	SkipBreaker bool
}

// Route is the legacy entry point. Use RouteWith for L4 support.
func (e *RouterEngine) Route(ctx context.Context, modelName string) (*RouteResult, error) {
	return e.RouteWith(ctx, modelName, RouteOptions{})
}

// RouteWith is the full routing pipeline: L1 static match → L2 breaker →
// L3 cost → L4 intent (if text supplied) → L5 Thompson → key selection.
// Delegates to the Chain-of-Responsibility pipeline defined in stage.go.
func (e *RouterEngine) RouteWith(ctx context.Context, modelName string, opts RouteOptions) (*RouteResult, error) {
	return e.routeWithPipeline(ctx, modelName, opts)
}

func (e *RouterEngine) RecordSuccess(channelID int64) {
	e.breaker.RecordSuccess(channelID)
	e.thompson.RecordSuccess(channelID)
	for _, obs := range e.getTrafficObs() {
		obs.OnRealSuccess(channelID)
	}
}

// RecordSuccessLatency records a successful call together with its
// measured duration, feeding both the breaker/Thompson posteriors
// and the per-channel EWMA latency tracker. Prefer this over
// RecordSuccess when the api layer has the duration at hand.
func (e *RouterEngine) RecordSuccessLatency(channelID int64, durationMs int64) {
	e.RecordSuccess(channelID)
	e.RecordLatency(channelID, durationMs)
}

// RecordFailure records a failed real call. status is the upstream
// HTTP status (or a synthesized one like 502/504 for network and
// stream failures); the circuit breaker buckets by it (401/404 hard
// reject, 429 short cooldown, 5xx consecutive failures).
func (e *RouterEngine) RecordFailure(channelID int64, status int) {
	e.breaker.RecordFailure(channelID, status)
	e.thompson.RecordFailure(channelID)
	for _, obs := range e.getTrafficObs() {
		obs.OnRealFailure(channelID)
	}
}

// RecordArmSuccess updates the auto-router's (tier, model) quality
// arm after a successful request. Arm keys are opaque strings
// ("tier:model"); see the auto package's ArmKey helper.
func (e *RouterEngine) RecordArmSuccess(key string) { e.thompson.RecordArmSuccess(key) }

// RecordArmFailure updates the auto-router's (tier, model) quality
// arm after a failed request.
func (e *RouterEngine) RecordArmFailure(key string) { e.thompson.RecordArmFailure(key) }

// RecordLatency updates the per-channel EWMA response time. Called
// by the api layer after each successful upstream call so the
// latency-aware routing strategies have fresh data.
func (e *RouterEngine) RecordLatency(channelID int64, durationMs int64) {
	if e.latency != nil {
		e.latency.Record(channelID, float64(durationMs))
	}
}

// LatencyTracker exposes the underlying tracker for the admin API
// and tests.
func (e *RouterEngine) LatencyTracker() *latency.Tracker { return e.latency }

// SetLatencyAlpha overrides the EWMA smoothing factor. Called at
// startup from config (latency_routing.alpha); 0 keeps the default.
func (e *RouterEngine) SetLatencyAlpha(alpha float64) {
	if e.latency != nil {
		e.latency.SetAlpha(alpha)
	}
}

// SetBlendWeights overrides the cost/latency weights for the
// cost_latency_blend strategy (latency_routing.cost_weight /
// latency_weight). Zero values keep the strategy defaults.
func (e *RouterEngine) SetBlendWeights(cost, latency float64) {
	e.cost.SetBlendWeights(cost, latency)
}

// Thompson returns the underlying sampler (for the admin API and
// tests).
func (e *RouterEngine) Thompson() *thompson.Sampler { return e.thompson }
