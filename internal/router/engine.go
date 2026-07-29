package router

import (
	"context"
	"time"

	"github.com/sn0wfree/llmRx/internal/intent"
	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
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
	static         *StaticRouter
	breaker        *CircuitBreaker
	cost           *CostRouter
	thompson       *thompson.Sampler
	intent         intent.Classifier
	pool           *pool.ChannelPool
	store          store.Store
	extraChannels  []func() []*model.Channel // BYOK hook (Phase 1.5 reserved)
	trafficObs     TrafficObserver            // optional real-traffic observer (prober etc.)
}

// TrafficObserver is the pluggable hook RouterEngine invokes on
// every successful or failed real (non-probe) call. Implementations
// must be safe for concurrent use. A nil observer (the default)
// silently drops the notification — keeps the hot path branchless
// when no prober is wired in.
type TrafficObserver interface {
	OnRealSuccess(channelID int64)
	OnRealFailure(channelID int64)
}

// SetTrafficObserver installs the observer. Pass nil to detach.
// Typically wired once during bootstrap from server.New().
func (e *RouterEngine) SetTrafficObserver(o TrafficObserver) {
	e.trafficObs = o
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
	return &RouterEngine{
		static:   NewStaticRouter(st),
		breaker:  NewCircuitBreaker(st),
		cost:     NewCostRouter(),
		thompson: thompson.New(thompson.Config{}),
		intent:   intent.Nop{},
		pool:     pool,
		store:    st,
	}
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
	Text         string              // last user message, used for L4 intent classification
	ModelSet     []string            // optional L1 override: match any of these models (combo load_balance)
	CostStrategy model.CostStrategy  // optional L3 override: "" = use global
}

// Route is the legacy entry point. Use RouteWith for L4 support.
func (e *RouterEngine) Route(ctx context.Context, modelName string) (*RouteResult, error) {
	return e.RouteWith(ctx, modelName, RouteOptions{})
}

// RouteWith is the full routing pipeline: L1 static → L2 breaker →
// L3 cost → L4 intent (if text supplied) → L5 Thompson.
func (e *RouterEngine) RouteWith(ctx context.Context, modelName string, opts RouteOptions) (*RouteResult, error) {
	start := time.Now()
	var logParts []string

	// L1: static match. When ModelSet is provided (combo
	// load_balance), expand the candidate set to all channels
	// that serve any model in the pool.
	var candidates []*model.Channel
	if len(opts.ModelSet) > 0 {
		candidates = e.static.MatchAny(opts.ModelSet)
		logParts = append(logParts, "L1(static,combo)")
	} else {
		candidates = e.static.Match(modelName)
		logParts = append(logParts, "L1(static)")
	}
	// Hook for Phase 1.5 BYOK: extra channel sources. Currently
	// no callbacks are registered so this is a no-op.
	for _, src := range e.extraChannels {
		candidates = append(candidates, src()...)
	}
	if len(candidates) == 0 {
		return nil, ErrNoChannel
	}

	candidates = e.breaker.Filter(candidates)
	logParts = append(logParts, "L2(breaker)")
	if len(candidates) == 0 {
		return nil, ErrAllBroken
	}

	// L3 cost sort. When CostStrategy is set (combo override),
	// use SortWith to avoid mutating global state.
	if opts.CostStrategy != "" {
		candidates = e.cost.SortWith(candidates, opts.CostStrategy)
		logParts = append(logParts, "L3(cost="+string(opts.CostStrategy)+")")
	} else {
		candidates = e.cost.Sort(candidates)
		logParts = append(logParts, "L3(cost)")
	}

	// L4: intent match. If we have a classifier and an input text,
	// bubble channels whose Intents list contains the predicted
	// intent to the front. Score is ignored for now — binary
	// inclusion is enough for the typical 4-6 label set.
	var intn intent.Intent
	if opts.Text != "" && e.intent != nil {
		intn = e.intent.Classify(opts.Text)
		if intn.Kind != "unknown" && intn.Kind != "general" && len(candidates) > 1 {
			matched, unmatched := splitByIntent(candidates, intn.Kind)
			if len(matched) > 0 {
				candidates = append(matched, unmatched...)
				logParts = append(logParts, "L4(intent="+intn.Kind+")")
			}
		}
	}

	// L5: Thompson sampling picks the winner. With a single
	// candidate this is a no-op; with several it uses the posterior
	// over success probability.
	if len(candidates) > 1 {
		ranked := e.thompson.Sample(candidates)
		logParts = append(logParts, "L5(thompson)")
		candidates = nil
		for _, r := range ranked {
			candidates = append(candidates, r.Channel)
		}
	}

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
		Intent:    intn,
	}

	logging.Debug("route",
		logging.F("model", modelName),
		logging.F("channel", ch.Name),
		logging.F("path", joinLog(logParts)),
		logging.F("duration_ms", time.Since(start).Milliseconds()),
	)
	return result, nil
}

// splitByIntent partitions channels by whether their Intents list
// contains the given kind. Order within each group is preserved.
func splitByIntent(channels []*model.Channel, kind string) (matched, unmatched []*model.Channel) {
	for _, c := range channels {
		if containsString(c.Intents, kind) {
			matched = append(matched, c)
		} else {
			unmatched = append(unmatched, c)
		}
	}
	return
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func (e *RouterEngine) RecordSuccess(channelID int64) {
	e.breaker.RecordSuccess(channelID)
	e.thompson.RecordSuccess(channelID)
	if e.trafficObs != nil {
		e.trafficObs.OnRealSuccess(channelID)
	}
}

func (e *RouterEngine) RecordFailure(channelID int64) {
	e.breaker.RecordFailure(channelID)
	e.thompson.RecordFailure(channelID)
	if e.trafficObs != nil {
		e.trafficObs.OnRealFailure(channelID)
	}
}

// Thompson returns the underlying sampler (for the admin API and
// tests).
func (e *RouterEngine) Thompson() *thompson.Sampler { return e.thompson }

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