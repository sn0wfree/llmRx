package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sn0wfree/llmRx/internal/broker"
	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/guardrail"
	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/observability"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/ratelimit"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/runtime"
	"github.com/sn0wfree/llmRx/internal/store"
)

type Handler struct {
	router    *router.RouterEngine
	pool      *pool.ChannelPool
	provider  provider.Provider // fallback (OpenAI) for tests
	providers map[string]provider.Provider
	cfg       *config.Config
	store     store.Store
	logBroker *broker.Broker[*model.Log]
	rt        *runtime.Defaults
	limits    *ratelimit.Limiter
	guardrails *guardrail.GuardrailEngine
}

func New(cfg *config.Config, eng *router.RouterEngine, cp *pool.ChannelPool, st store.Store, lb *broker.Broker[*model.Log], rt *runtime.Defaults) *Handler {
	if rt == nil {
		rt = runtime.New()
		rt.SetMarkupRatio(cfg.Server.MarkupRatio)
	}
	return &Handler{
		router:    eng,
		pool:      cp,
		provider:  provider.NewOpenAIProvider(),
		providers: provider.All(),
		cfg:       cfg,
		store:     st,
		logBroker: lb,
		rt:        rt,
		limits:    ratelimit.New(),
		guardrails: guardrail.New(st),
	}
}

// Limits exposes the rate limiter for the server to wire into the
// middleware. The limiter is process-local (in-memory sliding window).
func (h *Handler) Limits() *ratelimit.Limiter { return h.limits }

// SetStore wires the underlying store reference. Tests use this to
// inject a fake store; production wires the real SQLite.
func (h *Handler) SetStore(st store.Store) { h.store = st }

// Store returns the wired store; tests use it to assert log writes.
func (h *Handler) Store() store.Store { return h.store }

// lookupTokenInfo extracts the TokenInfo placed in the request
// context by middleware.Token. Returns ok=false when the request
// was authenticated without a TokenInfo in context (some unit tests
// bypass the middleware by going directly through a Handler method).
func lookupTokenInfo(ctx context.Context) (middleware.TokenInfo, bool) {
	v, ok := ctx.Value(middleware.TokenInfoKey).(middleware.TokenInfo)
	return v, ok
}

// providerFor returns the provider matching channel.Protocol,
// falling back to the default OpenAI provider if unknown.
// When streaming is false and retry/timeout are configured, the
// returned provider is wrapped with RetryingProvider for automatic
// retry + timeout. When streaming is true, the raw provider is
// returned so the streaming handler can correctly detect whether
// StreamingProvider is supported.
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

	// Wrap with retry + timeout when configured.
	retries := h.rt.MaxRetries()
	if retries <= 0 && h.rt.RequestTimeoutSec() <= 0 {
		return p
	}
	cfg := provider.RetryConfig{
		MaxRetries: int(retries),
		BaseDelay:  time.Duration(h.rt.RetryBaseDelayMs()) * time.Millisecond,
		Timeout:    time.Duration(h.rt.RequestTimeoutSec()) * time.Second,
	}
	return provider.NewRetryingProvider(p, cfg)
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

func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req provider.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
			h.handleCombo(w, r, &req, combo, info)
			return
		}
	}

	// Input guardrails: check request messages before routing.
	if gr := h.checkInputGuardrails(r, &req); gr != nil {
		writeError(w, http.StatusUnprocessableEntity, gr.Message, "guardrail_violated")
		return
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

	tokenID := lookupTokenID(r.Context(), h.store)

	if err != nil {
		h.router.RecordFailure(route.Channel.ID)
		writeError(w, statusCode, "upstream error: "+err.Error(), "upstream_error")
		h.emitLog(r.Context(), tokenID, req.Model, route, nil, duration, statusCode, true, h.clientIP(r))
		return
	}

	h.router.RecordSuccess(route.Channel.ID)
	h.emitLog(r.Context(), tokenID, req.Model, route, &resp.Usage, duration, statusCode, false, h.clientIP(r))
	writeJSON(w, resp)
}

// handleCombo dispatches a combo-model request. Non-streaming only
// in this iteration; streaming combos are deferred.
func (h *Handler) handleCombo(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest, combo model.TokenComboModel, info middleware.TokenInfo) {
	if req.Stream {
		// TODO: streaming combo support in future iteration
		writeError(w, http.StatusBadRequest, "streaming combo models is not yet supported", "combo_stream_not_supported")
		return
	}
	switch combo.Mode {
	case model.ComboModeSerial:
		h.handleSerialCombo(w, r, req, combo, info)
	default:
		h.handleLoadBalanceCombo(w, r, req, combo, info)
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
		h.emitLog(r.Context(), tokenID, req.Model, route, nil, 0, 0, true, h.clientIP(r))
		return
	}

	prov := h.providerFor(route.Channel.Protocol, false)
	start := time.Now()
	resp, statusCode, err := prov.Chat(r.Context(), req, route.KeyValue, route.Channel.BaseURL)
	duration := time.Since(start).Milliseconds()

	if err != nil {
		h.router.RecordFailure(route.Channel.ID)
		writeError(w, statusCode, "upstream error: "+err.Error(), "upstream_error")
		h.emitLog(r.Context(), tokenID, req.Model, route, nil, duration, statusCode, true, h.clientIP(r))
		return
	}

	h.router.RecordSuccess(route.Channel.ID)
	h.emitLog(r.Context(), tokenID, req.Model, route, &resp.Usage, duration, statusCode, false, h.clientIP(r))
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
			lastErr = fmt.Errorf("model %s: status=%d err=%w", modelName, statusCode, err)
			h.emitLog(r.Context(), tokenID, modelName, route, nil, duration, statusCode, true, h.clientIP(r))
			continue
		}

		// Success.
		h.router.RecordSuccess(route.Channel.ID)
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
	real := 0.0
	cached := 0
	if usage != nil {
		real = calcCost(route.Channel, *usage)
		if usage.PromptTokensDetails != nil {
			cached = usage.PromptTokensDetails.CachedTokens
		}
	}
	billed := h.billedCost(ctx, real)
	status := "ok"
	if failed {
		status = "fail"
	}
	log.Printf("log status=%s model=%s channel=%s key=%s prompt=%d completion=%d cached=%d real_usd=%.6f billed_usd=%.6f duration_ms=%d code=%d path=%s",
		status, modelName, route.Channel.Name, route.Key.KeyMasked,
		promptTokens(usage), completionTokens(usage), cached,
		real, billed,
		durationMs, statusCode, route.RouterLog,
	)
	entry := &model.Log{
		TokenID:         tokenID,
		ChannelID:       route.Channel.ID,
		KeyID:           route.Key.ID,
		Model:           modelName,
		PromptTokens:    promptTokens(usage),
		CompletionTokens: completionTokens(usage),
		CachedTokens:    cached,
		RealCostUSD:     real,
		BilledCostUSD:   billed,
		DurationMs:      durationMs,
		StatusCode:      statusCode,
		RouterPath:      route.RouterLog,
		RequestIP:       ip,
	}
	if err := h.store.CreateLog(entry); err != nil {
		log.Printf("warn: persist log: %v", err)
	}
	if h.logBroker != nil {
		h.logBroker.Publish(entry)
	}
	// Record Prometheus metrics.
	observability.RecordRequest(modelName, durationMs, failed, billed,
		promptTokens(usage), completionTokens(usage), false)
	// Credit both the per-token and per-plan spend ledgers in a
	// single SQL transaction. On ErrBudgetExceeded the token leg
	// is reverted atomically inside the store, so we no longer
	// need a manual compensation step here.
	planID := planIDFromContext(ctx)
	if tokenID > 0 && billed > 0 {
		if err := h.store.RecordRequestSpend(tokenID, planID, billed); err != nil {
			if errors.Is(err, store.ErrBudgetExceeded) {
				log.Printf("plan %d budget exceeded by $%.6f — request rejected, token ledger untouched",
					planID, billed)
			} else {
				log.Printf("warn: record request spend: %v", err)
			}
		}
	}
	// Account completion tokens against the rate limiter's TPM
	// budget so a stream of long completions eventually trips the
	// per-token ceiling.
	if tokenID > 0 && usage != nil && h.limits != nil {
		h.limits.Account(tokenID, usage.PromptTokens+usage.CompletionTokens)
	}
}

// billedCost returns the per-token / per-plan-adjusted billed cost.
// Server-wide markup is applied first; if the token has a Plan with
// a non-1.0 markup_ratio, it scales on top. Lookup is best-effort;
// a failed plan fetch falls back to the channel markup alone.
func (h *Handler) billedCost(ctx context.Context, real float64) float64 {
	base := real * h.Markup()
	if h.store == nil {
		return base
	}
	planID := planIDFromContext(ctx)
	if planID == 0 {
		return base
	}
	plan, err := h.store.GetPlan(planID)
	if err != nil || plan == nil || plan.MarkupRatio <= 0 {
		return base
	}
	return base * plan.MarkupRatio
}

// planIDFromContext extracts TokenInfo.PlanID from a request context.
func planIDFromContext(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	v, ok := ctx.Value(middleware.TokenInfoKey).(middleware.TokenInfo)
	if !ok {
		return 0
	}
	return v.PlanID
}

func promptTokens(u *provider.Usage) int {
	if u == nil {
		return 0
	}
	return u.PromptTokens
}

func completionTokens(u *provider.Usage) int {
	if u == nil {
		return 0
	}
	return u.CompletionTokens
}

func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	type modelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
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
				data = append(data, modelEntry{
					ID:      m,
					Object:  "model",
					Created: time.Now().Unix(),
					OwnedBy: ch.Provider,
				})
			}
		}
	}

	writeJSON(w, modelsResp{Object: "list", Data: data})
}

// Embeddings handles POST /v1/embeddings — OpenAI-compatible embedding
// vector generation. Follows the same routing and auth pipeline as
// ChatCompletions but proxies an EmbeddingsRequest instead.
func (h *Handler) Embeddings(w http.ResponseWriter, r *http.Request) {
	var req provider.EmbeddingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
			writeError(w, http.StatusForbidden, "model not allowed for this token", "model_not_allowed")
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
		writeError(w, statusCode, "upstream error: "+err.Error(), "upstream_error")
		h.emitLog(r.Context(), tokenID, req.Model, route, nil, duration, statusCode, true, h.clientIP(r))
		return
	}

	h.router.RecordSuccess(route.Channel.ID)
	h.emitLog(r.Context(), tokenID, req.Model, route, &resp.Usage, duration, statusCode, false, h.clientIP(r))
	writeJSON(w, resp)
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
	prompt := float64(usage.PromptTokens)
	cached := 0.0
	if usage.PromptTokensDetails != nil {
		cached = float64(usage.PromptTokensDetails.CachedTokens)
		if cached > prompt {
			cached = prompt
		}
	}
	normal := (prompt - cached) / 1000000.0 * ch.InputPrice
	cachedCost := 0.0
	if ch.CachedInputDiscount > 0 {
		cachedCost = cached / 1000000.0 * ch.InputPrice * ch.CachedInputDiscount
	}
	output := float64(usage.CompletionTokens) / 1000000.0 * ch.OutputPrice
	return normal + cachedCost + output
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
		h.emitLog(r.Context(), lookupTokenID(r.Context(), h.store), req.Model, route, nil,
			time.Since(start).Milliseconds(), http.StatusBadGateway, true, h.clientIP(r))
		return
	}

	var usage *provider.Usage
	flushed := 0
	bytesSent := int64(0)
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
				fmt.Fprintf(w, "event: error\ndata: {\"message\":%q}\n\n", ev.Err.Error())
				flusher.Flush()
				h.emitLog(r.Context(), lookupTokenID(r.Context(), h.store), req.Model, route, usage,
					time.Since(start).Milliseconds(), http.StatusBadGateway, true, h.clientIP(r))
				return
			}
			if ev.Chunk.Usage != nil {
				usage = ev.Chunk.Usage
			}
			payload, err := json.Marshal(ev.Chunk)
			if err != nil {
				continue
			}
			line := fmt.Sprintf("data: %s\n\n", payload)
			n, werr := fmt.Fprint(w, line)
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
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
	h.router.RecordSuccess(route.Channel.ID)
	h.emitLog(r.Context(), lookupTokenID(r.Context(), h.store), req.Model, route, usage,
		time.Since(start).Milliseconds(), http.StatusOK, false, h.clientIP(r))
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
