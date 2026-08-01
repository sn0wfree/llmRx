package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sn0wfree/llmRx/internal/broker"
	"github.com/sn0wfree/llmRx/internal/cache"
	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/guardrail"
	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/modelmeta"
	"github.com/sn0wfree/llmRx/internal/observability"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/ratelimit"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/runtime"
	"github.com/sn0wfree/llmRx/internal/store"

	proberpkg "github.com/sn0wfree/llmRx/internal/prober"
)

// chunkBufMaxBytes caps the buffer we return to the pool. A typical
// SSE chunk is well under 4 KiB; a stray 10 MB streaming response
// should not stay pinned in the pool until the next GC.
const chunkBufMaxBytes = 64 * 1024

var chunkBufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

// putChunkBuf returns buf to the pool only if its capacity stays
// under the cap. Otherwise the buffer is dropped, allowing the GC
// to reclaim the oversized memory.
func putChunkBuf(buf *bytes.Buffer) {
	if buf.Cap() <= chunkBufMaxBytes {
		chunkBufPool.Put(buf)
	}
}

var (
	dataPrefix  = []byte("data: ")
	dataSuffix  = []byte("\n\n")
	donePayload = []byte("data: [DONE]\n\n")
	eventPrefix = []byte("event: error\ndata: ")
	eventSuffix = []byte("\n\n")
)

// modelsCacheTTL is how long a /models response stays fresh.
const modelsCacheTTL = 30 * time.Second

type modelsCacheEntry struct {
	data      []byte
	expiresAt time.Time
}

type retryingCache struct {
	cfg     provider.RetryConfig
	wrapped map[string]provider.Provider
}

type Handler struct {
	router    *router.RouterEngine
	pool      *pool.ChannelPool
	provider  provider.Provider // fallback (OpenAI) for tests
	providers map[string]provider.Provider
	cfg       *config.Config
	store     store.Store
	logStore  *logstore.Manager
	logBroker *broker.Broker[*model.Log]
	rt        *runtime.Defaults
	limits    *ratelimit.Limiter
	guardrails *guardrail.GuardrailEngine
	prober    *proberpkg.Cache
	// retryingCache is an atomic snapshot of the RetryingProvider
	// cache. The read path (every non-streaming request) is a single
	// atomic.Load — lock-free. The write path (admin config change)
	// atomically swaps the whole snapshot.
	retryingCache atomic.Value // holds *retryingCache

	// modelsCache caches the /models response for 30s to avoid
	// scanning all channels on every ListModels call.
	modelsCache   atomic.Value // holds *modelsCacheEntry

	// Extracted components for single responsibility.
	costCalc *CostCalculator
	emitter  *LogEmitter

	// responseCache caches LLM responses for deterministic
	// requests (temperature=0). Set via SetCache; nil means
	// caching is disabled.
	responseCache cache.Cache
}

func New(cfg *config.Config, eng *router.RouterEngine, cp *pool.ChannelPool, st store.Store, ls *logstore.Manager, lb *broker.Broker[*model.Log], rt *runtime.Defaults) *Handler {
	if rt == nil {
		rt = runtime.New()
		rt.SetMarkupRatio(cfg.Server.MarkupRatio)
	}
	cc := NewCostCalculator(func() float64 { return rt.MarkupRatio() })
	lim := ratelimit.New()
	h := &Handler{
		router:     eng,
		pool:       cp,
		provider:   provider.NewOpenAIProvider(),
		providers:  provider.All(),
		cfg:        cfg,
		store:      st,
		logStore:   ls,
		logBroker:  lb,
		rt:         rt,
		limits:     lim,
		guardrails: guardrail.New(st),
		costCalc:   cc,
		emitter:    NewLogEmitter(ls, lb, st, lim, cc),
	}
	return h
}

// Limits exposes the rate limiter for the server to wire into the
// middleware. The limiter is process-local (in-memory sliding window).
func (h *Handler) Limits() *ratelimit.Limiter { return h.limits }

// SetStore wires the underlying store reference. Tests use this to
// inject a fake store; production wires the real SQLite.
func (h *Handler) SetStore(st store.Store) { h.store = st }

// Store returns the wired store; tests use it to assert log writes.
func (h *Handler) Store() store.Store { return h.store }

// SetCache wires the response cache. Nil means caching is disabled.
func (h *Handler) SetCache(c cache.Cache) { h.responseCache = c }

// lookupTokenInfo extracts the TokenInfo placed in the request
// context by middleware.Token. Returns ok=false when the request
// was authenticated without a TokenInfo in context (some unit tests
// bypass the middleware by going directly through a Handler method).
func lookupTokenInfo(ctx context.Context) (middleware.TokenInfo, bool) {
	v, ok := ctx.Value(middleware.TokenInfoKey).(middleware.TokenInfo)
	return v, ok
}

// findDefaultCombo returns the "default" combo for a token, used by
// the model="auto" shortcut. Priority:
//  1. combo with IsDefault=true and Enabled
//  2. combo named "auto" and Enabled
//  3. first enabled combo (by map iteration order, non-deterministic)
//
// Returns ok=false when no enabled combo exists.
func findDefaultCombo(info middleware.TokenInfo) (model.TokenComboModel, bool) {
	var namedAuto, first model.TokenComboModel
	var hasNamedAuto, hasFirst bool
	for name, c := range info.ComboModels {
		if !c.Enabled {
			continue
		}
		if c.IsDefault {
			return c, true
		}
		if name == "auto" {
			namedAuto = c
			hasNamedAuto = true
			continue
		}
		if !hasFirst {
			first = c
			hasFirst = true
		}
	}
	if hasNamedAuto {
		return namedAuto, true
	}
	return first, hasFirst
}

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
	wrapped := provider.NewRetryingProvider(p, cfg)
	snap := &retryingCache{
		cfg:     cfg,
		wrapped: map[string]provider.Provider{channelProtocol: wrapped},
	}
	// Carry forward any existing cache entries to avoid losing
	// previously-wrapped protocols.
	if v := h.retryingCache.Load(); v != nil {
		c := v.(*retryingCache)
		snap.wrapped[channelProtocol] = wrapped
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

type errorResp struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

func errorTypeFor(status int) string {
	switch {
	case status == http.StatusBadRequest:
		return "invalid_request_error"
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return "invalid_request_error"
	case status == http.StatusNotFound:
		return "invalid_request_error"
	case status >= 500:
		return "api_error"
	default:
		return "upstream_error"
	}
}

func writeError(w http.ResponseWriter, status int, msg, code string) {
	// Defensive: a status of 0 means the upstream provider never
	// produced an HTTP response (DNS failure, connection refused,
	// TLS handshake error). The net/http package rejects
	// WriteHeader(0) with "invalid WriteHeader code" — which
	// otherwise surfaces as a panic recovered by chi middleware
	// and shows up to the client as a 0-byte 200 response.
	if status <= 0 {
		status = http.StatusBadGateway
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := errorResp{}
	resp.Error.Message = msg
	resp.Error.Type = errorTypeFor(status)
	resp.Error.Code = code
	_ = json.NewEncoder(w).Encode(resp)
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

// readBody reads the full request body, capped at limit bytes.
// The body is replaced with a fresh reader so downstream code
// can still read from r.Body if needed.
func readBody(r *http.Request, limit int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, limit)
	b, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	return b, nil
}

// mustMarshalJSON marshals v to JSON, panicking on error. Used
// in the cache path where the response is guaranteed to marshal
// successfully since it came from the upstream.
func mustMarshalJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("api: mustMarshalJSON: " + err.Error())
	}
	return b
}

// defaultRequestBodyMaxBytes is the fallback cap applied by
// bodyLimit() when the operator did not configure
// request_body_max_bytes. 64 MiB comfortably fits multimodal
// OpenAI requests with embedded base64 images.
const defaultRequestBodyMaxBytes int64 = 64 * 1024 * 1024

// bodyLimit returns the effective MaxBytesReader cap for the
// current Handler. 0 disables the cap (the decoder can then grow
// unbounded — almost always a bug).
func (h *Handler) bodyLimit() int64 {
	if h.cfg != nil && h.cfg.Server.RequestBodyMaxBytes > 0 {
		return h.cfg.Server.RequestBodyMaxBytes
	}
	return defaultRequestBodyMaxBytes
}

// decodeJSONBody wraps r.Body in MaxBytesReader (so an over-sized
// upload is rejected with 413 before the decoder allocates) and
// decodes into out. On *http.MaxBytesError the caller sees a
// distinct sentinel via errors.As so it can produce the right
// response shape.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, limit int64, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	return json.NewDecoder(r.Body).Decode(out)
}

// isBodyTooLarge reports whether err came from MaxBytesReader
// refusing to read past the limit.
//
// Go 1.19+ exposes the limit as *http.MaxBytesError; Go 1.18 (the
// minimum version declared by go.mod) returns a plain error whose
// message matches the canonical stdlib string. We match on the
// message because that string is part of the documented stdlib
// contract for both versions.
func isBodyTooLarge(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "http: request body too large")
}

func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	// Read raw body for cache control parsing before decoding.
	bodyLimit := h.bodyLimit()
	rawBody, err := readBody(r, bodyLimit)
	if err != nil {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds limit", "body_too_large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error(), "invalid_body")
		return
	}

	var req provider.ChatRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error(), "invalid_body")
		return
	}

	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "model is required", "missing_model")
		return
	}

	// Per-token model whitelist + IP whitelist enforcement.
	var tokenID int64
	if info, ok := lookupTokenInfo(r.Context()); ok {
		tokenID = info.ID
		if !info.HasModelAccess(req.Model) {
			logging.Warn("api.model_denied",
				logging.F("model", req.Model),
				logging.F("token_id", info.ID),
				logging.F("endpoint", "chat/completions"),
			)
			writeError(w, http.StatusForbidden, "model not allowed for this token", "model_not_allowed")
			return
		}
		ip := h.clientIP(r)
		if !info.HasIPAccess(ip) {
			writeError(w, http.StatusForbidden, "ip not allowed for this token", "ip_not_allowed")
			return
		}

		// Combo model dispatch: if the request model matches a
		// combo defined on this token, route via the combo path
		// instead of the standard L1-L5 pipeline.
		if combo, ok := info.ComboModels[req.Model]; ok && combo.Enabled {
			logging.Debug("chat.combo_dispatch",
				logging.F("model", req.Model),
				logging.F("combo_name", combo.Name),
				logging.F("combo_models", combo.Models),
			)
			h.handleCombo(w, r, &req, combo, info)
			return
		}

		if req.Model == "auto" {
			if combo, ok := findDefaultCombo(info); ok {
				logging.Debug("chat.auto_resolved",
					logging.F("combo_name", combo.Name),
					logging.F("is_default", combo.IsDefault),
					logging.F("combo_models", combo.Models),
				)
				h.handleCombo(w, r, &req, combo, info)
				return
			}
			logging.Warn("chat.auto_no_combo", logging.F("token_id", info.ID))
			writeError(w, http.StatusNotFound, "no auto combo configured for this token", "no_auto_combo")
			return
		}
	}

	// Input guardrails: check request messages before routing.
	if gr := h.checkInputGuardrails(r, &req); gr != nil {
		writeError(w, http.StatusUnprocessableEntity, gr.Message, "guardrail_violated")
		return
	}

	// Cache lookup: check cache before routing (non-streaming).
	if h.responseCache != nil && !req.Stream {
		cc := cache.ParseCacheControl(rawBody)
		if cc == nil || !cc.NoStore {
			ckey, ckeyErr := cache.Key(&req)
			if ckeyErr == nil {
				if cc != nil && cc.NoCache {
					// no-cache: skip read, but still write below.
				} else {
					if cached, hit, _ := h.responseCache.Get(r.Context(), ckey); hit {
						w.Header().Set("X-LlmRx-Cache", "HIT")
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(cached.StatusCode)
						_, _ = w.Write(cached.Body)
						return
					}
				}
			}
		}
	}

	// Streaming cache lookup: check cache before streaming.
	if h.responseCache != nil && req.Stream {
		cc := cache.ParseCacheControl(rawBody)
		if cc == nil || !cc.NoStore {
			ckey, ckeyErr := cache.Key(&req)
			if ckeyErr == nil {
				if cc != nil && cc.NoCache {
					// no-cache: skip read, but still write below.
				} else {
					if cached, hit, _ := h.responseCache.Get(r.Context(), ckey); hit {
						w.Header().Set("X-LlmRx-Cache", "HIT")
						w.Header().Set("Content-Type", "text/event-stream")
						w.Header().Set("Cache-Control", "no-cache")
						w.Header().Set("Connection", "keep-alive")
						w.Header().Set("X-Accel-Buffering", "no")
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write(cached.Body)
						if f, ok := w.(http.Flusher); ok {
							f.Flush()
						}
						return
					}
				}
			}
		}
	}

	if req.Stream {
		h.streamChatCompletions(w, r, &req)
		return
	}

	route, err := h.router.RouteWith(context.Background(), req.Model, router.RouteOptions{Text: lastUserText(req.Messages)})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "no available channel: "+err.Error(), "no_channel")
		return
	}

	prov := h.providerFor(route.Channel.Protocol, false)
	start := time.Now()
	resp, statusCode, err := prov.Chat(r.Context(), &req, route.KeyValue, route.Channel.BaseURL)
	duration := time.Since(start).Milliseconds()

	if tokenID == 0 {
		tokenID = lookupTokenID(r.Context(), h.store)
	}

	if err != nil {
		h.router.RecordFailure(route.Channel.ID)
		observability.RecordUpstreamError(req.Model, statusCode)
		writeError(w, statusCode, "upstream error: "+err.Error(), "upstream_error")
		h.emitLog(r.Context(), tokenID, req.Model, route, nil, duration, statusCode, true, h.clientIP(r))
		return
	}

	h.router.RecordSuccess(route.Channel.ID)

	// Output guardrails: check response content before returning.
	if gr := h.checkOutputGuardrails(r, resp); gr != nil {
		writeError(w, http.StatusUnprocessableEntity, gr.Message, "guardrail_violated")
		return
	}

	// Cache the successful response.
	if h.responseCache != nil {
		cc := cache.ParseCacheControl(rawBody)
		if cc == nil || !cc.NoStore {
			ckey, ckeyErr := cache.Key(&req)
			if ckeyErr == nil {
				ttl := time.Duration(h.cfg.Server.CacheTTLSeconds) * time.Second
				entry := &cache.Entry{
					Key:        ckey,
					StatusCode: http.StatusOK,
					Body:       mustMarshalJSON(resp),
					Usage:      &resp.Usage,
					CostUSD:    h.costCalc.RealCost(route.Channel, resp.Usage),
					ChannelID:  route.Channel.ID,
				}
				_ = h.responseCache.Set(r.Context(), entry, ttl)
			}
		}
	}

	h.emitLog(r.Context(), tokenID, req.Model, route, &resp.Usage, duration, statusCode, false, h.clientIP(r))
	writeJSON(w, resp)
}

// handleCombo dispatches a combo-model request. Streaming and non-
// streaming are both supported; load_balance uses the L1-L5 pipeline
// to pick one channel, serial tries each underlying model in order.
func (h *Handler) handleCombo(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest, combo model.TokenComboModel, info middleware.TokenInfo) {
	if req.Stream {
		h.handleStreamCombo(w, r, req, combo)
		return
	}
	switch combo.Mode {
	case model.ComboModeSerial:
		h.handleSerialCombo(w, r, req, combo, info)
	default:
		h.handleLoadBalanceCombo(w, r, req, combo, info)
	}
}

// handleStreamCombo routes a streaming combo-model request. For
// load_balance it uses the L1-L5 pipeline with the combo's model pool;
// for serial it tries each model in order, streaming from the first
// that yields an upstream SSE channel.
func (h *Handler) handleStreamCombo(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest, combo model.TokenComboModel) {
	if combo.Mode == model.ComboModeSerial {
		h.handleStreamSerialCombo(w, r, req, combo)
		return
	}
	// load_balance: pick one channel via L1-L5 from the combo's model set,
	// then rewrite req.Model to the resolved channel model and stream.
	opts := router.RouteOptions{
		Text:         lastUserText(req.Messages),
		ModelSet:     combo.Models,
		CostStrategy: combo.Strategy,
	}
	route, err := h.router.RouteWith(context.Background(), req.Model, opts)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "no available channel: "+err.Error(), "no_channel")
		return
	}
	original := req.Model
	if len(route.Channel.Models) > 0 {
		req.Model = route.Channel.Models[0]
	}
	logging.Debug("combo.stream_dispatch",
		logging.F("model_requested", original),
		logging.F("model_actual", req.Model),
		logging.F("channel", route.Channel.Name),
	)
	h.streamChatCompletions(w, r, req)
	req.Model = original
}

// handleStreamSerialCombo tries each model in combo.Models in order.
// First model that produces a successful upstream SSE channel wins;
// remaining models are skipped. If all fail, returns 502.
func (h *Handler) handleStreamSerialCombo(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest, combo model.TokenComboModel) {
	opts := router.RouteOptions{Text: lastUserText(req.Messages)}
	var lastErr error
	for _, modelName := range combo.Models {
		route, routeErr := h.router.RouteWith(r.Context(), modelName, opts)
		if routeErr != nil {
			lastErr = routeErr
			continue
		}
		prov := h.providerFor(route.Channel.Protocol, true)
		if _, ok := prov.(provider.StreamingProvider); !ok {
			lastErr = fmt.Errorf("model %s: streaming not supported", modelName)
			continue
		}
		// Rewrite the request model so the upstream sees the real model
		// name (combo name is a virtual alias).
		original := req.Model
		req.Model = modelName
		logging.Debug("combo.stream_serial.success",
			logging.F("model", modelName),
			logging.F("channel", route.Channel.Name),
		)
		h.streamChatCompletions(w, r, req)
		req.Model = original
		_ = route // route recorded via streamChatCompletions -> emitLog
		return
	}
	if lastErr != nil {
		logging.Warn("combo.stream_serial.all_failed",
			logging.F("combo_models", combo.Models),
			logging.F("error", lastErr),
		)
		writeError(w, http.StatusBadGateway, "all combo models failed: "+lastErr.Error(), "combo_all_failed")
	} else {
		writeError(w, http.StatusServiceUnavailable, "no channel matched for combo models", "no_channel")
	}
}

// handleLoadBalanceCombo routes a combo-model request through the
// existing L1-L5 pipeline with the combo's model pool as the L1
// candidate set and optional L3 strategy override.
func (h *Handler) handleLoadBalanceCombo(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest, combo model.TokenComboModel, info middleware.TokenInfo) {
	tokenID := lookupTokenID(r.Context(), h.store)
	opts := router.RouteOptions{
		Text:         lastUserText(req.Messages),
		ModelSet:     combo.Models,
		CostStrategy: combo.Strategy,
	}
	route, err := h.router.RouteWith(context.Background(), req.Model, opts)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "no available channel: "+err.Error(), "no_channel")
		return
	}

	// Rewrite req.Model to the real channel model so the upstream
	// sees a model name it recognises (the combo name — e.g. "auto"
	// — is only meaningful to llmRx).
	originalModel := req.Model
	if len(route.Channel.Models) > 0 {
		req.Model = route.Channel.Models[0]
	}

	// Probe-based short-circuit: if the prober has recently
	// verified this channel as unhealthy, fail fast with a
	// distinct error so callers can distinguish "no channel" from
	// "selected channel looks dead". Only triggers when both
	// (a) the request is model="auto" and (b) the cache has a
	// fresh failing entry — otherwise we'd over-aggressively
	// penalise non-auto traffic.
	if h.prober != nil && originalModel == "auto" && !h.prober.Healthy(route.Channel.ID) {
		lat, _ := h.prober.Latest(route.Channel.ID)
		logging.Warn("chat.probe_unhealthy",
			logging.F("channel_id", route.Channel.ID),
			logging.F("channel", route.Channel.Name),
			logging.F("error", lat.Error),
			logging.F("checked_at", lat.CheckedAt),
		)
		writeError(w, http.StatusBadGateway,
			"selected channel marked unhealthy by probe: "+lat.Error,
			"probe_unhealthy")
		return
	}

	prov := h.providerFor(route.Channel.Protocol, false)
	start := time.Now()
	resp, statusCode, err := prov.Chat(r.Context(), req, route.KeyValue, route.Channel.BaseURL)
	duration := time.Since(start).Milliseconds()

	req.Model = originalModel

	if err != nil {
		h.router.RecordFailure(route.Channel.ID)
		observability.RecordUpstreamError(originalModel, statusCode)
		logging.Warn("combo.upstream_error",
			logging.F("model_requested", originalModel),
			logging.F("model_actual", req.Model),
			logging.F("channel", route.Channel.Name),
			logging.F("status", statusCode),
			logging.F("error", err.Error()),
		)
		writeError(w, statusCode, "upstream error: "+err.Error(), "upstream_error")
		h.emitLog(r.Context(), tokenID, originalModel, route, nil, duration, statusCode, true, h.clientIP(r))
		return
	}

	h.router.RecordSuccess(route.Channel.ID)
	logging.Debug("combo.upstream_success",
		logging.F("model_requested", originalModel),
		logging.F("model_actual", route.Channel.Models[0]),
		logging.F("channel", route.Channel.Name),
		logging.F("status", statusCode),
		logging.F("duration_ms", duration),
	)
	h.emitLog(r.Context(), tokenID, originalModel, route, &resp.Usage, duration, statusCode, false, h.clientIP(r))
	writeJSON(w, resp)
}

// handleSerialCombo tries each underlying model in order. First
// successful 2xx response wins; failures trigger L2 breaker.
func (h *Handler) handleSerialCombo(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest, combo model.TokenComboModel, info middleware.TokenInfo) {
	tokenID := lookupTokenID(r.Context(), h.store)
	opts := router.RouteOptions{Text: lastUserText(req.Messages)}
	var lastErr error

	for _, modelName := range combo.Models {
		// Try to route to a channel for this model.
		route, routeErr := h.router.RouteWith(context.Background(), modelName, opts)
		if routeErr != nil {
			// No channel or all broken for this model; skip.
			lastErr = routeErr
			continue
		}

		// Attempt the actual chat.
		prov := h.providerFor(route.Channel.Protocol, false)
		start := time.Now()
		resp, statusCode, err := prov.Chat(r.Context(), req, route.KeyValue, route.Channel.BaseURL)
		duration := time.Since(start).Milliseconds()

		if err != nil || statusCode >= 500 {
			h.router.RecordFailure(route.Channel.ID)
			observability.RecordUpstreamError(modelName, statusCode)
			lastErr = fmt.Errorf("model %s: status=%d err=%w", modelName, statusCode, err)
			logging.Debug("combo.serial.fail",
				logging.F("model", modelName),
				logging.F("channel", route.Channel.Name),
				logging.F("status", statusCode),
				logging.F("error", err),
			)
			h.emitLog(r.Context(), tokenID, modelName, route, nil, duration, statusCode, true, h.clientIP(r))
			continue
		}

		// Success.
		h.router.RecordSuccess(route.Channel.ID)
		logging.Debug("combo.serial.success",
			logging.F("model", modelName),
			logging.F("channel", route.Channel.Name),
			logging.F("status", statusCode),
			logging.F("duration_ms", duration),
		)
		h.emitLog(r.Context(), tokenID, modelName, route, &resp.Usage, duration, statusCode, false, h.clientIP(r))
		writeJSON(w, resp)
		return
	}

	// All models failed.
	if lastErr != nil {
		writeError(w, http.StatusBadGateway, "all combo models failed: "+lastErr.Error(), "combo_all_failed")
	} else {
		writeError(w, http.StatusServiceUnavailable, "no channel matched for combo models", "no_channel")
	}
}

// clientIP returns the best-effort source IP for the request,
// consulting proxy headers only when the operator has opted
// in AND the immediate peer is in the trusted proxy CIDR list.
//
// Defaults to r.RemoteAddr so a direct-internet deployment
// can't be spoofed into bypassing per-token IP whitelists via
// X-Forwarded-For.
func (h *Handler) clientIP(r *http.Request) string {
	if h.cfg != nil && h.cfg.Server.TrustProxyHeaders {
		if v := h.firstTrustedProxyHeader(r); v != "" {
			return v
		}
	}
	return r.RemoteAddr
}

// firstTrustedProxyHeader returns the leftmost X-Forwarded-For
// or X-Real-IP value when r.RemoteAddr is in the configured
// trusted CIDR list. Returns "" otherwise.
func (h *Handler) firstTrustedProxyHeader(r *http.Request) string {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = r.RemoteAddr
	}
	if !h.peerIsTrustedProxy(remoteHost) {
		return ""
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		// XFF is a comma-separated chain leftmost=client;
		// trim spaces and return the leftmost entry.
		if i := strings.IndexByte(v, ','); i >= 0 {
			v = v[:i]
		}
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

// peerIsTrustedProxy reports whether the configured CIDR list
// trusts the immediate peer. An empty list under TrustProxyHeaders
// is treated as "trust every source" so the operator can opt in
// for tightly-controlled environments where every peer is a
// reverse proxy (e.g. K8s behind a single service mesh).
func (h *Handler) peerIsTrustedProxy(remoteHost string) bool {
	if len(h.cfg.Server.TrustedProxyCIDRs) == 0 {
		return true
	}
	ip := net.ParseIP(remoteHost)
	if ip == nil {
		return false
	}
	for _, cidr := range h.cfg.Server.TrustedProxyCIDRs {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func lookupTokenID(ctx context.Context, st store.Store) int64 {
	v, ok := ctx.Value(middleware.TokenIDKey).(int64)
	if !ok {
		return 0
	}
	return v
}

func (h *Handler) emitLog(ctx context.Context, tokenID int64, modelName string, route *router.RouteResult, usage *provider.Usage, durationMs int64, statusCode int, failed bool, ip string) {
	h.emitter.Emit(ctx, tokenID, modelName, route, usage, durationMs, statusCode, failed, ip)
}

// billedCost returns the per-token / per-plan-adjusted billed cost.
// Server-wide markup is applied first; if the token has a Plan with
// a non-1.0 markup_ratio, it scales on top. The per-plan markup is
// cached in TokenInfo.PlanMarkupRatio (populated by
// tokencache.Reload), so the hot path is allocation-free.
func (h *Handler) billedCost(ctx context.Context, real float64) float64 {
	base := real * h.Markup()
	if ctx == nil {
		return base
	}
	info, ok := lookupTokenInfo(ctx)
	if !ok || info.PlanMarkupRatio <= 0 {
		return base
	}
	return base * info.PlanMarkupRatio
}

func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	details := r.URL.Query().Get("details") == "true"

	// Cache hit: return cached response for non-details requests.
	if !details {
		if v := h.modelsCache.Load(); v != nil {
			entry := v.(*modelsCacheEntry)
			if time.Now().Before(entry.expiresAt) {
				w.Header().Set("Content-Type", "application/json")
				w.Write(entry.data)
				return
			}
		}
	}

	type modelPricing struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	}

	type modelEntry struct {
		ID           string                     `json:"id"`
		Object       string                     `json:"object"`
		Created      int64                      `json:"created"`
		OwnedBy      string                     `json:"owned_by"`
		ContextWindow *int                      `json:"context_window,omitempty"`
		MaxOutput    *int                       `json:"max_output,omitempty"`
		Pricing      *modelPricing              `json:"pricing,omitempty"`
		Capabilities *modelmeta.ModelCapabilities `json:"capabilities,omitempty"`
		Modalities   []string                   `json:"modalities,omitempty"`
	}

	type modelsResp struct {
		Object string       `json:"object"`
		Data   []modelEntry `json:"data"`
	}

	var data []modelEntry
	seen := make(map[string]bool)
	for _, ch := range h.pool.GetAllChannels() {
		for _, m := range ch.Models {
			if !seen[m] {
				seen[m] = true
				entry := modelEntry{
					ID:      m,
					Object:  "model",
					Created: time.Now().Unix(),
					OwnedBy: ch.Provider,
				}
				if details {
					if meta, ok := modelmeta.Get(m); ok {
						cw := meta.ContextWindow
						mo := meta.MaxOutput
						entry.ContextWindow = &cw
						entry.MaxOutput = &mo
						entry.Pricing = &modelPricing{
							Input:  meta.InputPrice,
							Output: meta.OutputPrice,
						}
						caps := meta.Capabilities
						entry.Capabilities = &caps
						entry.Modalities = meta.Modalities
					}
				}
				data = append(data, entry)
			}
		}
	}

	data = append(data, modelEntry{
		ID:      "auto",
		Object:  "model",
		Created: time.Now().Unix(),
		OwnedBy: "llmrx",
	})

	resp := modelsResp{Object: "list", Data: data}
	payload, _ := json.Marshal(resp)

	// Cache non-details responses for 30s.
	if !details {
		h.modelsCache.Store(&modelsCacheEntry{
			data:      payload,
			expiresAt: time.Now().Add(modelsCacheTTL),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(payload)
}

// Embeddings handles POST /v1/embeddings — OpenAI-compatible embedding
// vector generation. Follows the same routing and auth pipeline as
// ChatCompletions but proxies an EmbeddingsRequest instead.
func (h *Handler) Embeddings(w http.ResponseWriter, r *http.Request) {
	var req provider.EmbeddingsRequest
	if err := decodeJSONBody(w, r, h.bodyLimit(), &req); err != nil {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds limit", "body_too_large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error(), "invalid_body")
		return
	}

	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "model is required", "missing_model")
		return
	}

	// Per-token model whitelist + IP whitelist enforcement.
	if info, ok := lookupTokenInfo(r.Context()); ok {
		if !info.HasModelAccess(req.Model) {
			logging.Warn("api.model_denied", logging.F("model", req.Model), logging.F("token_id", info.ID), logging.F("endpoint", "embeddings")); writeError(w, http.StatusForbidden, "model not allowed for this token", "model_not_allowed")
			return
		}
		ip := h.clientIP(r)
		if !info.HasIPAccess(ip) {
			writeError(w, http.StatusForbidden, "ip not allowed for this token", "ip_not_allowed")
			return
		}
	}

	route, err := h.router.RouteWith(context.Background(), req.Model, router.RouteOptions{})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "no available channel: "+err.Error(), "no_channel")
		return
	}

	// Check that the upstream provider supports embeddings.
	prov := h.providerFor(route.Channel.Protocol, false)
	embProv, ok := prov.(provider.EmbeddingsProvider)
	if !ok {
		writeError(w, http.StatusBadRequest, "upstream provider does not support embeddings", "embeddings_not_supported")
		return
	}

	start := time.Now()
	resp, statusCode, err := embProv.Embeddings(r.Context(), &req, route.KeyValue, route.Channel.BaseURL)
	duration := time.Since(start).Milliseconds()

	tokenID := lookupTokenID(r.Context(), h.store)

	if err != nil {
		h.router.RecordFailure(route.Channel.ID)
		observability.RecordUpstreamError(req.Model, statusCode)
		writeError(w, statusCode, "upstream error: "+err.Error(), "upstream_error")
		h.emitLog(r.Context(), tokenID, req.Model, route, nil, duration, statusCode, true, h.clientIP(r))
		return
	}

	h.router.RecordSuccess(route.Channel.ID)
	h.emitLog(r.Context(), tokenID, req.Model, route, &resp.Usage, duration, statusCode, false, h.clientIP(r))
	writeJSON(w, resp)
}

// ImageGenerations handles POST /v1/images/generations. Routes the
// request via the standard L1-L5 pipeline (model name = req.Model)
// and forwards to the upstream's image-generation endpoint.
func (h *Handler) ImageGenerations(w http.ResponseWriter, r *http.Request) {
	var req provider.ImagesRequest
	if err := decodeJSONBody(w, r, h.bodyLimit(), &req); err != nil {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds limit", "body_too_large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error(), "invalid_body")
		return
	}
	if req.Model == "" {
		req.Model = "dall-e-3"
	}
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt is required", "missing_prompt")
		return
	}
	if info, ok := lookupTokenInfo(r.Context()); ok {
		if !info.HasModelAccess(req.Model) {
			logging.Warn("api.model_denied", logging.F("model", req.Model), logging.F("token_id", info.ID), logging.F("endpoint", "images/generations")); writeError(w, http.StatusForbidden, "model not allowed for this token", "model_not_allowed")
			return
		}
	}
	route, err := h.router.RouteWith(context.Background(), req.Model, router.RouteOptions{})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "no available channel: "+err.Error(), "no_channel")
		return
	}
	prov := h.providerFor(route.Channel.Protocol, false)
	ip, ok := prov.(provider.ImagesProvider)
	if !ok {
		writeError(w, http.StatusBadRequest, "upstream provider does not support images", "images_not_supported")
		return
	}
	start := time.Now()
	resp, statusCode, err := ip.Images(r.Context(), &req, route.KeyValue, route.Channel.BaseURL)
	duration := time.Since(start).Milliseconds()
	tokenID := lookupTokenID(r.Context(), h.store)
	if err != nil {
		h.router.RecordFailure(route.Channel.ID)
		observability.RecordUpstreamError(req.Model, statusCode)
		writeError(w, statusCode, "upstream error: "+err.Error(), "upstream_error")
		h.emitLog(r.Context(), tokenID, req.Model, route, nil, duration, statusCode, true, h.clientIP(r))
		return
	}
	h.router.RecordSuccess(route.Channel.ID)
	var usage *provider.Usage
	if resp.Usage != nil {
		usage = resp.Usage
	}
	h.emitLog(r.Context(), tokenID, req.Model, route, usage, duration, statusCode, false, h.clientIP(r))
	writeJSON(w, resp)
}

// AudioSpeech handles POST /v1/audio/speech. Returns raw audio bytes
// (Content-Type from upstream, typically audio/mpeg).
func (h *Handler) AudioSpeech(w http.ResponseWriter, r *http.Request) {
	var req provider.AudioSpeechRequest
	if err := decodeJSONBody(w, r, h.bodyLimit(), &req); err != nil {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds limit", "body_too_large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error(), "invalid_body")
		return
	}
	if req.Model == "" {
		req.Model = "tts-1"
	}
	if req.Input == "" {
		writeError(w, http.StatusBadRequest, "input is required", "missing_input")
		return
	}
	if req.Voice == "" {
		req.Voice = "alloy"
	}
	if info, ok := lookupTokenInfo(r.Context()); ok {
		if !info.HasModelAccess(req.Model) {
			logging.Warn("api.model_denied", logging.F("model", req.Model), logging.F("token_id", info.ID), logging.F("endpoint", "audio/speech")); writeError(w, http.StatusForbidden, "model not allowed for this token", "model_not_allowed")
			return
		}
	}
	route, err := h.router.RouteWith(context.Background(), req.Model, router.RouteOptions{})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "no available channel: "+err.Error(), "no_channel")
		return
	}
	prov := h.providerFor(route.Channel.Protocol, false)
	ap, ok := prov.(provider.AudioProvider)
	if !ok {
		writeError(w, http.StatusBadRequest, "upstream provider does not support audio", "audio_not_supported")
		return
	}
	start := time.Now()
	audio, statusCode, err := ap.Speech(r.Context(), &req, route.KeyValue, route.Channel.BaseURL)
	duration := time.Since(start).Milliseconds()
	tokenID := lookupTokenID(r.Context(), h.store)
	if err != nil {
		h.router.RecordFailure(route.Channel.ID)
		observability.RecordUpstreamError(req.Model, statusCode)
		writeError(w, statusCode, "upstream error: "+err.Error(), "upstream_error")
		h.emitLog(r.Context(), tokenID, req.Model, route, nil, duration, statusCode, true, h.clientIP(r))
		return
	}
	h.router.RecordSuccess(route.Channel.ID)
	h.emitLog(r.Context(), tokenID, req.Model, route, nil, duration, statusCode, false, h.clientIP(r))
	format := req.ResponseFormat
	if format == "" {
		format = "mp3"
	}
	ct := "audio/" + format
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", itoa(len(audio)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(audio)
}

// AudioTranscriptions handles POST /v1/audio/transcriptions. Expects
// multipart/form-data with a 'file' field and 'model' field.
func (h *Handler) AudioTranscriptions(w http.ResponseWriter, r *http.Request) {
	// Cap the multipart upload so a malicious caller can't stream
	// gigabytes into the temp spool. ParseMultipartForm keeps its
	// own 32 MiB in-memory ceiling; MaxBytesReader bounds total
	// bytes (in-memory + temp file).
	if limit := h.bodyLimit(); limit > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds limit", "body_too_large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid multipart form: "+err.Error(), "invalid_body")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file is required", "missing_file")
		return
	}
	defer file.Close()
	audioData, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read file: "+err.Error(), "invalid_body")
		return
	}
	req := provider.AudioTranscriptionRequest{
		Model:    r.FormValue("model"),
		Language: r.FormValue("language"),
		Prompt:   r.FormValue("prompt"),
		Format:   r.FormValue("response_format"),
	}
	if req.Model == "" {
		req.Model = "whisper-1"
	}
	if info, ok := lookupTokenInfo(r.Context()); ok {
		if !info.HasModelAccess(req.Model) {
			logging.Warn("api.model_denied", logging.F("model", req.Model), logging.F("token_id", info.ID), logging.F("endpoint", "audio/transcriptions")); writeError(w, http.StatusForbidden, "model not allowed for this token", "model_not_allowed")
			return
		}
	}
	route, err := h.router.RouteWith(context.Background(), req.Model, router.RouteOptions{})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "no available channel: "+err.Error(), "no_channel")
		return
	}
	prov := h.providerFor(route.Channel.Protocol, false)
	ap, ok := prov.(provider.AudioProvider)
	if !ok {
		writeError(w, http.StatusBadRequest, "upstream provider does not support audio", "audio_not_supported")
		return
	}
	mime := header.Header.Get("Content-Type")
	if mime == "" {
		mime = "audio/mpeg"
	}
	start := time.Now()
	resp, statusCode, err := ap.Transcription(r.Context(), &req, audioData, mime, route.KeyValue, route.Channel.BaseURL)
	duration := time.Since(start).Milliseconds()
	tokenID := lookupTokenID(r.Context(), h.store)
	if err != nil {
		h.router.RecordFailure(route.Channel.ID)
		observability.RecordUpstreamError(req.Model, statusCode)
		writeError(w, statusCode, "upstream error: "+err.Error(), "upstream_error")
		h.emitLog(r.Context(), tokenID, req.Model, route, nil, duration, statusCode, true, h.clientIP(r))
		return
	}
	h.router.RecordSuccess(route.Channel.ID)
	var usage *provider.Usage
	if resp.Duration > 0 {
		usage = &provider.Usage{TotalTokens: int(resp.Duration * 1000)}
	}
	h.emitLog(r.Context(), tokenID, req.Model, route, usage, duration, statusCode, false, h.clientIP(r))
	writeJSON(w, resp)
}

// Rerank handles POST /v1/rerank. Cohere-compatible.
func (h *Handler) Rerank(w http.ResponseWriter, r *http.Request) {
	var req provider.RerankRequest
	if err := decodeJSONBody(w, r, h.bodyLimit(), &req); err != nil {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds limit", "body_too_large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error(), "invalid_body")
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "model is required", "missing_model")
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required", "missing_query")
		return
	}
	if len(req.Documents) == 0 {
		writeError(w, http.StatusBadRequest, "documents is required", "missing_documents")
		return
	}
	if info, ok := lookupTokenInfo(r.Context()); ok {
		if !info.HasModelAccess(req.Model) {
			logging.Warn("api.model_denied", logging.F("model", req.Model), logging.F("token_id", info.ID), logging.F("endpoint", "rerank")); writeError(w, http.StatusForbidden, "model not allowed for this token", "model_not_allowed")
			return
		}
	}
	route, err := h.router.RouteWith(context.Background(), req.Model, router.RouteOptions{})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "no available channel: "+err.Error(), "no_channel")
		return
	}
	prov := h.providerFor(route.Channel.Protocol, false)
	rp, ok := prov.(provider.RerankProvider)
	if !ok {
		writeError(w, http.StatusBadRequest, "upstream provider does not support rerank", "rerank_not_supported")
		return
	}
	start := time.Now()
	resp, statusCode, err := rp.Rerank(r.Context(), &req, route.KeyValue, route.Channel.BaseURL)
	duration := time.Since(start).Milliseconds()
	tokenID := lookupTokenID(r.Context(), h.store)
	if err != nil {
		h.router.RecordFailure(route.Channel.ID)
		observability.RecordUpstreamError(req.Model, statusCode)
		writeError(w, statusCode, "upstream error: "+err.Error(), "upstream_error")
		h.emitLog(r.Context(), tokenID, req.Model, route, nil, duration, statusCode, true, h.clientIP(r))
		return
	}
	h.router.RecordSuccess(route.Channel.ID)
	var usage *provider.Usage
	if resp.Usage != nil {
		usage = resp.Usage
	}
	h.emitLog(r.Context(), tokenID, req.Model, route, usage, duration, statusCode, false, h.clientIP(r))
	writeJSON(w, resp)
}

// itoa formats an int as decimal string without importing strconv
// just for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// calcCost returns the real USD cost of a single chat completion.
// The cached-token discount applies only to the prompt leg: when the
// upstream reports that some prompt tokens were served from its
// prompt cache (Anthropic, OpenAI GPT-5+, etc.), the gateway charges
// only the discount fraction of InputPrice for those tokens. The
// discount is configured per channel (CachedInputDiscount). If the
// discount is zero, no savings apply (and cached tokens still count
// toward PromptTokens for billing purposes).
func calcCost(ch *model.Channel, usage provider.Usage) float64 {
	cc := CostCalculator{}
	return cc.RealCost(ch, usage)
}



// streamChatCompletions is invoked when the client sets stream=true.
// It performs the normal route selection, asks the upstream for a
// stream, and writes each chunk back as an SSE event. The
// Content-Type is text/event-stream and the response is flushed
// after every chunk so the client sees tokens as they're produced.
//
// Two server-side caps protect against resource exhaustion:
//
//   - stream_timeout_sec   total wall-clock for the stream;
//                          context is cancelled when it elapses.
//   - stream_max_body_bytes soft cap on bytes written to the client;
//                          when exceeded the stream terminates with a
//                          "limit_exceeded" error frame and the
//                          channel is recorded as a failure.
//
// A value of 0 disables the corresponding cap.
func (h *Handler) streamChatCompletions(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest) {
	route, err := h.router.RouteWith(r.Context(), req.Model, router.RouteOptions{Text: lastUserText(req.Messages)})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "no available channel: "+err.Error(), "no_channel")
		return
	}
	prov := h.providerFor(route.Channel.Protocol, true)
	sp, ok := prov.(provider.StreamingProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "streaming not supported by protocol "+route.Channel.Protocol, "stream_unsupported")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming requires http.Flusher", "no_flusher")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	start := time.Now()
	observability.StreamStart()
	defer observability.StreamEnd()
	// Build a single derived context. Use WithTimeout when the
	// admin has configured a non-zero cap (default 5m); otherwise
	// the request context alone is enough — no extra WithCancel
	// wrapper is needed, so we avoid creating an unobservable
	// goroutine and overwriting its cancel func (the prior
	// implementation leaked one WithCancel per request).
	var (
		ctx    = r.Context()
		cancel context.CancelFunc = func() {}
	)
	if timeout := h.streamTimeout(); timeout > 0 {
		ctx, cancel = context.WithTimeout(r.Context(), timeout)
	}
	defer cancel()
	maxBody := int64(h.streamMaxBodyBytes())

	ch, err := sp.StreamChat(ctx, req, route.KeyValue, route.Channel.BaseURL)
	if err != nil {
		// Emit a single error frame and bail.
		fmt.Fprintf(w, "event: error\ndata: {\"message\":%q}\n\n", err.Error())
		flusher.Flush()
		h.router.RecordFailure(route.Channel.ID)
		observability.RecordUpstreamError(req.Model, http.StatusBadGateway)
		h.emitLog(r.Context(), lookupTokenID(r.Context(), h.store), req.Model, route, nil,
			time.Since(start).Milliseconds(), http.StatusBadGateway, true, h.clientIP(r))
		return
	}

	var (
		usage           *provider.Usage
		flushed         = 0
		bytesSent       = int64(0)
		contentBuilder  strings.Builder
		streamBuf       bytes.Buffer
	)
	for {
		select {
		case <-ctx.Done():
			reason := "client disconnected"
			if ctx.Err() == context.DeadlineExceeded {
				reason = "stream timeout exceeded"
			}
			h.router.RecordFailure(route.Channel.ID)
			fmt.Fprintf(w, "event: error\ndata: {\"message\":%q}\n\n", reason)
			flusher.Flush()
			h.emitLog(r.Context(), lookupTokenID(r.Context(), h.store), req.Model, route, usage,
				time.Since(start).Milliseconds(), http.StatusGatewayTimeout, true, h.clientIP(r))
			return
		case ev, ok := <-ch:
			if !ok {
				// upstream closed cleanly.
				goto done
			}
			if ev.Err != nil {
				h.router.RecordFailure(route.Channel.ID)
				observability.RecordUpstreamError(req.Model, http.StatusBadGateway)
				fmt.Fprintf(w, "event: error\ndata: {\"message\":%q}\n\n", ev.Err.Error())
				flusher.Flush()
				h.emitLog(r.Context(), lookupTokenID(r.Context(), h.store), req.Model, route, usage,
					time.Since(start).Milliseconds(), http.StatusBadGateway, true, h.clientIP(r))
				return
			}
			if ev.Chunk.Usage != nil {
				usage = ev.Chunk.Usage
			}
			// Accumulate delta content for output guardrail check.
			for _, c := range ev.Chunk.Choices {
				contentBuilder.WriteString(c.Delta.ContentString())
			}
			buf := chunkBufPool.Get().(*bytes.Buffer)
			buf.Reset()
			buf.Write(dataPrefix)
			if err := json.NewEncoder(buf).Encode(ev.Chunk); err != nil {
				buf.Reset()
				putChunkBuf(buf)
				continue
			}
			// json.Encoder.Encode appends a trailing '\n'; the SSE
			// frame needs "\n\n" so add one more.
			if b := buf.Bytes(); len(b) > 0 && b[len(b)-1] == '\n' {
				buf.WriteByte('\n')
			} else {
				buf.Write(dataSuffix)
			}
			chunkBytes := buf.Bytes()
			streamBuf.Write(chunkBytes)
			n, werr := w.Write(chunkBytes)
			putChunkBuf(buf)
			bytesSent += int64(n)
			if werr != nil {
				cancel()
				return
			}
			if maxBody > 0 && bytesSent >= maxBody {
				fmt.Fprintf(w, "event: error\ndata: {\"message\":%q}\n\n", "stream max body bytes exceeded")
				flusher.Flush()
				h.router.RecordFailure(route.Channel.ID)
				h.emitLog(r.Context(), lookupTokenID(r.Context(), h.store), req.Model, route, usage,
					time.Since(start).Milliseconds(), http.StatusRequestEntityTooLarge, true, h.clientIP(r))
				return
			}
			// Flush every 4 chunks or on the final one to keep latency
			// low without burning CPU.
			flushed++
			if flushed%4 == 0 {
				flusher.Flush()
			}
		}
	}
done:
	if h.guardrails != nil {
		tokenID := lookupTokenID(r.Context(), h.store)
		if gr := h.guardrails.CheckOutput(r.Context(), contentBuilder.String(), tokenID); gr != nil {
			fmt.Fprintf(w, "event: error\ndata: {\"message\":%q}\n\n", gr.Message)
			flusher.Flush()
			h.emitLog(r.Context(), tokenID, req.Model, route, usage,
				time.Since(start).Milliseconds(), http.StatusOK, true, h.clientIP(r))
			return
		}
	}
	streamBuf.Write(donePayload)
	w.Write(donePayload)
	flusher.Flush()
	h.router.RecordSuccess(route.Channel.ID)
	h.tryCacheStream(r.Context(), req, route, usage, streamBuf.Bytes())
	h.emitLog(r.Context(), lookupTokenID(r.Context(), h.store), req.Model, route, usage,
		time.Since(start).Milliseconds(), http.StatusOK, false, h.clientIP(r))
}

// tryCacheStream attempts to cache the accumulated SSE stream body.
// Errors are logged but not returned — the stream has already been
// delivered to the client.
func (h *Handler) tryCacheStream(ctx context.Context, req *provider.ChatRequest, route *router.RouteResult, usage *provider.Usage, body []byte) {
	if h.responseCache == nil {
		return
	}
	maxBody := h.cfg.Server.CacheMaxBodyBytes
	if maxBody > 0 && len(body) > maxBody {
		return
	}
	ckey, err := cache.Key(req)
	if err != nil {
		return
	}
	ttl := time.Duration(h.cfg.Server.CacheTTLSeconds) * time.Second
	var costUSD float64
	if usage != nil {
		costUSD = h.costCalc.RealCost(route.Channel, *usage)
	}
	entry := &cache.Entry{
		Key:        ckey,
		StatusCode: http.StatusOK,
		Body:       body,
		Usage:      usage,
		CostUSD:    costUSD,
		ChannelID:  route.Channel.ID,
}
	if err := h.responseCache.Set(ctx, entry, ttl); err != nil {
		logging.Warn("cache stream set failed", logging.F("error", err.Error()))
	}
}

// streamTimeout returns the per-stream wall-clock cap. Reads the
// current value from runtime.Defaults so admin updates take effect
// without a restart. Defaults to 5 minutes when rt is unset or zero.
func (h *Handler) streamTimeout() time.Duration {
	if h.rt == nil {
		return 5 * time.Minute
	}
	if sec := h.rt.StreamTimeoutSec(); sec > 0 {
		return time.Duration(sec) * time.Second
	}
	return 5 * time.Minute
}

// streamMaxBodyBytes returns the soft cap on bytes the gateway will
// emit to the streaming client. 0 = unlimited. Reads from rt so
// admin updates are live.
func (h *Handler) streamMaxBodyBytes() int {
	if h.rt == nil {
		return 32 << 20
	}
	if n := h.rt.StreamMaxBodyBytes(); n >= 0 {
		return int(n)
	}
	return 32 << 20
}

// lastUserText returns the last user-role message in the conversation
// for L4 intent classification. If no user message is present, the
// empty string is returned, which disables L4.
func lastUserText(msgs []provider.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].ContentString()
		}
	}
	return ""
}

// checkInputGuardrails evaluates input guardrail rules against the
// request messages. Returns nil if all pass, or a Result on failure.
func (h *Handler) checkInputGuardrails(r *http.Request, req *provider.ChatRequest) *guardrail.Result {
	if h.guardrails == nil {
		return nil
	}
	var texts []string
	for _, m := range req.Messages {
		texts = append(texts, m.ContentString())
	}
	tokenID := lookupTokenID(r.Context(), h.store)
	return h.guardrails.CheckInput(r.Context(), texts, tokenID)
}

// checkOutputGuardrails evaluates output guardrail rules against the
// response text. Returns nil if all pass, or a Result on failure.
func (h *Handler) checkOutputGuardrails(r *http.Request, resp *provider.ChatResponse) *guardrail.Result {
	if h.guardrails == nil || resp == nil {
		return nil
	}
	var texts []string
	for _, c := range resp.Choices {
		texts = append(texts, c.Message.ContentString())
	}
	if len(texts) == 0 {
		return nil
	}
	tokenID := lookupTokenID(r.Context(), h.store)
	return h.guardrails.CheckOutput(r.Context(), strings.Join(texts, "\n"), tokenID)
}
