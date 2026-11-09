package api

import (
	"sync"
	"time"

	chi "github.com/go-chi/chi/v5"
	"github.com/sn0wfree/llmRx/internal/broker"
	"github.com/sn0wfree/llmRx/internal/cache"
	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/guardrail"
	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/mcp"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/modelcost"
	"github.com/sn0wfree/llmRx/internal/pool"
	proberpkg "github.com/sn0wfree/llmRx/internal/prober"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/ratelimit"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/router/auto"
	"github.com/sn0wfree/llmRx/internal/router/session"
	"github.com/sn0wfree/llmRx/internal/runtime"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/usage"
	"net/http"
	"sync/atomic"
)

type Handler struct {
	router     *router.RouterEngine
	pool       *pool.ChannelPool
	provider   provider.Provider // fallback (OpenAI) for tests
	providers  map[string]provider.Provider
	cfg        *config.Config
	store      store.Store
	logStore   *logstore.Manager
	logBroker  *broker.Broker[*model.Log]
	rt         *runtime.Defaults
	limits     *ratelimit.Limiter
	guardrails *guardrail.GuardrailEngine
	prober     *proberpkg.Cache
	// retryingCache is an atomic snapshot of the RetryingProvider
	// cache. The read path (every non-streaming request) is a single
	// atomic.Load — lock-free. The write path (admin config change)
	// atomically swaps the whole snapshot.
	retryingCache atomic.Value // holds *retryingCache
	retryingMu    sync.Mutex   // serializes cache rebuilds to prevent lost entries

	// modelsCache caches the /models response for 30s to avoid
	// scanning all channels on every ListModels call.
	modelsCache atomic.Value // holds *modelsCacheEntry

	// Extracted components for single responsibility.
	costCalc *CostCalculator
	emitter  *LogEmitter

	// responseCache caches LLM responses for deterministic
	// requests (temperature=0). Set via SetCache; nil means
	// caching is disabled.
	responseCache cache.Cache

	// mcpLoop is the agentic tool dispatch loop. Set via SetMCPLoop;
	// nil means MCP tool routing is disabled.
	mcpLoop *mcp.AgenticLoop

	// autoPool drives Thompson arm sampling for mode:auto combos;
	// autoScorer classifies prompts into complexity tiers. Swap the
	// scorer via SetAutoScorer (the LLM classifier is wired from
	// config in New).
	autoPool   *auto.Pool
	autoScorer auto.ComplexityScorer
	// autoStats accumulates decision counters for the admin state
	// endpoint; reset via admin /reload.
	autoStats *auto.Stats
	// sessions pins multi-turn conversations to one model/channel
	// when session affinity is enabled. nil disables affinity.
	sessions *session.Store

	// Task-aware routing (auto upgrade): taskClassifier identifies
	// the request task (task_source=auto); taskModelBias reorders
	// tier candidates per task; contModel remembers the model each
	// session last used so long-context sessions can continue with
	// it (tool-chain continuity).
	taskClassifier auto.TaskClassifier
	taskSource     string
	taskModelBias  map[string][]string
	continuationOn bool
	contThreshold  int
	contMu         sync.Mutex
	contModel      map[string]string

	// regLimiter throttles public registration per IP.
	regLimiter *registerLimiter

	// redeemLimiter throttles top-up code redemption per IP so a
	// single account can't brute-force guess codes at high rate.
	redeemLimiter *registerLimiter

	// tokenReload refreshes the bearer-token cache after
	// registration creates a new token (the cache is owned by the
	// server). nil = no refresh (tests).
	tokenReload func() error

	// modelCosts is the in-memory rate card (the model_costs table).
	// Billing resolves the price from here; a model absent from the
	// cache is rejected fail-closed before the upstream call.
	modelCosts *modelcost.Cache
}

func New(cfg *config.Config, eng *router.RouterEngine, cp *pool.ChannelPool, st store.Store, ls *logstore.Manager, lb *broker.Broker[*model.Log], rt *runtime.Defaults) *Handler {
	if rt == nil {
		rt = runtime.New()
		rt.SetMarkupRatio(cfg.Server.MarkupRatio)
	}
	cc := NewCostCalculator(func() float64 { return rt.MarkupRatio() })
	lim := ratelimit.New()
	mc := modelcost.New(st)
	h := &Handler{
		router:         eng,
		pool:           cp,
		provider:       provider.NewOpenAIProvider(),
		providers:      provider.All(),
		cfg:            cfg,
		store:          st,
		logStore:       ls,
		logBroker:      lb,
		rt:             rt,
		limits:         lim,
		guardrails:     guardrail.New(st),
		costCalc:       cc,
		emitter:        NewLogEmitter(ls, lb, st, lim, cc, mc, cfg.Server.LogSampleRate),
		autoPool:       auto.NewPool(eng.Thompson()),
		autoScorer:     buildAutoScorer(cfg),
		autoStats:      auto.NewStats(),
		taskClassifier: auto.NewTaskClassifier(),
		taskSource:     cfg.AutoRouter.TaskSource,
		taskModelBias:  cfg.AutoRouter.TaskModelBias,
		contModel:      make(map[string]string),
		regLimiter:     newRegisterLimiter(5, time.Hour),
		redeemLimiter:  newRegisterLimiter(20, time.Hour),
		modelCosts:     mc,
	}
	if cfg.AutoRouter.SessionContinuation {
		h.continuationOn = true
		h.contThreshold = cfg.AutoRouter.ContinuationThreshold
		if h.contThreshold <= 0 {
			h.contThreshold = 100000
		}
	}
	return h
}

// buildAutoScorer assembles the complexity classifier from config:
// heuristic scorer with (optional) custom tier thresholds, wrapped
// by the LLM classifier when an endpoint is configured.
func buildAutoScorer(cfg *config.Config) auto.ComplexityScorer {
	th := auto.DefaultThresholds()
	if cfg != nil && len(cfg.AutoRouter.TierThresholds) == 3 {
		th = auto.Thresholds{cfg.AutoRouter.TierThresholds[0], cfg.AutoRouter.TierThresholds[1], cfg.AutoRouter.TierThresholds[2]}
	}
	heuristic := auto.NewHeuristicScorer(th)
	if cfg != nil && cfg.AutoRouter.LLMClassifier.Enabled && cfg.AutoRouter.LLMClassifier.BaseURL != "" {
		llmCfg := auto.LLMClassifierConfig{
			BaseURL: cfg.AutoRouter.LLMClassifier.BaseURL,
			APIKey:  cfg.AutoRouter.LLMClassifier.APIKey,
			Model:   cfg.AutoRouter.LLMClassifier.Model,
			Timeout: time.Duration(cfg.AutoRouter.LLMClassifier.TimeoutSec * float64(time.Second)),
		}
		return auto.NewLLMClassifier(llmCfg, heuristic)
	}
	return heuristic
}

// AutoStats exposes the decision counter accumulator so the admin
// handler can serve it and reset it via /reload.
func (h *Handler) AutoStats() *auto.Stats { return h.autoStats }

// SetAutoScorer replaces the complexity classifier used by
// mode:auto combos. The default is the heuristic scorer; the
// optional LLM classifier is swapped in via this setter.
func (h *Handler) SetAutoScorer(s auto.ComplexityScorer) {
	if s != nil {
		h.autoScorer = s
	}
}

// SetSessions wires the session-affinity store. nil disables
// affinity (default). Started at server startup from config.
func (h *Handler) SetSessions(s *session.Store) { h.sessions = s }

// Sessions exposes the affinity store for the admin state endpoint.
func (h *Handler) Sessions() *session.Store { return h.sessions }

// Limits exposes the rate limiter for the server to wire into the
// middleware. The limiter is process-local unless a cluster backend
// (PGWindowBackend) was injected via SetLimiter.
func (h *Handler) Limits() *ratelimit.Limiter { return h.limits }

// SetLimiter swaps in a limiter built with a cluster-shared window
// backend (P12 M2: PG minute buckets). Must be called before the
// middleware chain is mounted; New() already created a default
// process-local limiter for single-node deployments.
func (h *Handler) SetLimiter(lim *ratelimit.Limiter) {
	if lim != nil {
		h.limits = lim
	}
}

// SetUsageTracker attaches the P21 per-channel plan-quota tracker to
// the emitter. Called once at startup; nil disables tracking.
func (h *Handler) SetUsageTracker(t *usage.Tracker) {
	h.emitter.SetUsageTracker(t)
}

// SetStore wires the underlying store reference. Tests use this to
// inject a fake store; production wires the real SQLite.
func (h *Handler) SetStore(st store.Store) { h.store = st }

// Store returns the wired store; tests use it to assert log writes.
func (h *Handler) Store() store.Store { return h.store }

// SetCache wires the response cache. Nil means caching is disabled.
func (h *Handler) SetCache(c cache.Cache) { h.responseCache = c }

func (h *Handler) SetMCPLoop(a *mcp.AgenticLoop) { h.mcpLoop = a }

// GuardrailEngine returns the guardrail engine for admin wiring.
func (h *Handler) GuardrailEngine() *guardrail.GuardrailEngine { return h.guardrails }

// providerFor returns the provider matching channel.Protocol,
// falling back to the default OpenAI provider if unknown.
// When streaming is false and retry/timeout are configured, the
// returned provider is wrapped with RetryingProvider for automatic
// retry + timeout. When streaming is true, the raw provider is
// returned so the streaming handler can correctly detect whether
// StreamingProvider is supported.
//
// The wrapped provider is cached per-protocol so the hot path does
// not allocate a fresh RetryingProvider struct on every request.
// The cache is invalidated when the runtime retry/timeout config
// changes (which is rare — only via the admin UI).
func (h *Handler) providerFor(channelProtocol string, streaming bool) provider.Provider {
	var p provider.Provider
	if pp, ok := h.providers[channelProtocol]; ok {
		p = pp
	} else {
		p = h.provider
	}

	// For streaming requests, return the raw provider so the
	// type assertion to StreamingProvider works correctly.
	if streaming {
		return p
	}

	// No retry/timeout configured — return raw provider.
	retries := h.rt.MaxRetries()
	if retries <= 0 && h.rt.RequestTimeoutSec() <= 0 {
		return p
	}
	cfg := provider.RetryConfig{
		MaxRetries: int(retries),
		BaseDelay:  time.Duration(h.rt.RetryBaseDelayMs()) * time.Millisecond,
		Timeout:    time.Duration(h.rt.RequestTimeoutSec()) * time.Second,
	}

	// Lock-free read: check the atomic cache snapshot.
	if v := h.retryingCache.Load(); v != nil {
		c := v.(*retryingCache)
		if c.cfg == cfg {
			if cached, ok := c.wrapped[channelProtocol]; ok {
				return cached
			}
		}
	}

	// Cache miss: (re)build the wrapped provider for this protocol.
	// Serialize rebuilds so two concurrent misses don't overwrite
	// each other's protocol entries.
	h.retryingMu.Lock()
	defer h.retryingMu.Unlock()
	// Double-check after acquiring the lock.
	if v := h.retryingCache.Load(); v != nil {
		c := v.(*retryingCache)
		if c.cfg == cfg {
			if cached, ok := c.wrapped[channelProtocol]; ok {
				return cached
			}
		}
	}
	wrapped := provider.NewRetryingProvider(p, cfg)
	snap := &retryingCache{
		cfg:     cfg,
		wrapped: map[string]provider.Provider{channelProtocol: wrapped},
	}
	// Carry forward any existing cache entries to avoid losing
	// previously-wrapped protocols.
	if v := h.retryingCache.Load(); v != nil {
		c := v.(*retryingCache)
		for k, v := range c.wrapped {
			snap.wrapped[k] = v
		}
	}
	h.retryingCache.Store(snap)
	return wrapped
}

// InvalidateRetryWrappers clears the cached RetryingProvider instances
// so the next providerFor() call rebuilds them. Call after admin UI
// changes to retry/timeout config.
func (h *Handler) InvalidateRetryWrappers() {
	h.retryingCache.Store(&retryingCache{cfg: provider.RetryConfig{}, wrapped: make(map[string]provider.Provider)})
}

// InvalidateModelsCache forces the next /models call to rebuild the
// response. Call after channels are added/removed/updated.
func (h *Handler) InvalidateModelsCache() {
	h.modelsCache.Store((*modelsCacheEntry)(nil))
}

// Markup returns the current per-request billing multiplier.
func (h *Handler) Markup() float64 { return h.rt.MarkupRatio() }

// SetMarkup atomically replaces the current multiplier.
func (h *Handler) SetMarkup(m float64) { h.rt.SetMarkupRatio(m) }

// SetProvider swaps the upstream client. Production wires the real
// OpenAI-compatible HTTP client; tests inject a mock to script
// responses and observe call args. Note: this only affects channels
// whose Protocol is "openai" or empty; use SetProviders to swap
// per-protocol clients.
func (h *Handler) SetProvider(p provider.Provider) { h.provider = p }

// SetProber attaches the channel health-probe cache. The cache is
// consulted on every auto-model request so a recent failed probe
// short-circuits the call before it burns upstream latency. nil
// disables the check.
func (h *Handler) SetProber(p *proberpkg.Cache) { h.prober = p }

// SetProviders replaces the per-protocol provider map. Used by
// tests to inject mocks for every protocol. Pass a nil-valued
// entry to skip a protocol (fall through to the default).
func (h *Handler) SetProviders(m map[string]provider.Provider) {
	h.providers = m
}

// Routes returns a subrouter mounting the public chat API. The caller
// is responsible for attaching auth middleware (server.go wires the
// Token middleware on the parent engine).
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Post("/chat/completions", h.ChatCompletions)
	r.Post("/embeddings", h.Embeddings)
	r.Post("/images/generations", h.ImageGenerations)
	r.Post("/audio/speech", h.AudioSpeech)
	r.Post("/audio/transcriptions", h.AudioTranscriptions)
	r.Post("/rerank", h.Rerank)
	r.Get("/models", h.ListModels)
	return r
}

// SetModelCosts swaps the rate-card cache (used after admin edits /
// startup seeding so billing picks up fresh prices).
func (h *Handler) SetModelCosts(c *modelcost.Cache) { h.modelCosts = c }

// ReloadModelCosts re-reads the rate card from the store (used after
// admin writes / tests).
func (h *Handler) ReloadModelCosts() error {
	if h.modelCosts == nil {
		return nil
	}
	return h.modelCosts.Reload()
}

// ModelCosts exposes the shared rate-card cache (wired into the
// admin handler so admin edits can reload it).
func (h *Handler) ModelCosts() *modelcost.Cache { return h.modelCosts }
