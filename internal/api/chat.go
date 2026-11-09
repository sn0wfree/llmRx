package api

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	json "github.com/goccy/go-json"
	"github.com/sn0wfree/llmRx/internal/cache"
	"github.com/sn0wfree/llmRx/internal/guardrail"
	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/observability"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/router"
	"net/http"
)

var (
	dataPrefix  = []byte("data: ")
	dataSuffix  = []byte("\n\n")
	donePayload = []byte("data: [DONE]\n\n")
	eventPrefix = []byte("event: error\ndata: ")
	eventSuffix = []byte("\n\n")
)

// toolCallAccum accumulates streaming delta.tool_calls keyed by
// Index, reassembling fragments from multiple SSE chunks into a
// complete tool call.
type toolCallAccum struct {
	Index     int
	ID        string
	Name      strings.Builder
	Arguments strings.Builder
}

var toolCallAccumPool = sync.Pool{
	New: func() any { return &toolCallAccum{} },
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

	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages array must not be empty", "missing_messages")
		return
	}

	// Carry the request's image count for the cost calculator (image
	// pricing). The wrapped context propagates to all emit sites.
	r = r.WithContext(ctxWithImages(r.Context(), &req))

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
						// Bill + log the cache hit (all prompt tokens
						// treated as cache-read) and count it in usage.
						if cached.Usage != nil {
							h.emitter.EmitCacheHit(r.Context(),
								lookupTokenID(r.Context(), h.store),
								req.Model, cached.Usage, h.clientIP(r))
						}
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
						if cached.Usage != nil {
							h.emitter.EmitCacheHit(r.Context(),
								lookupTokenID(r.Context(), h.store),
								req.Model, cached.Usage, h.clientIP(r))
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

	// Fail-closed: the concrete model must be on the rate card before
	// any upstream spend. (Combo/auto models are checked in handleCombo.)
	if !h.requirePriced(w, req.Model) {
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
		h.router.RecordFailure(route.Channel.ID, statusCode)
		observability.RecordUpstreamError(req.Model, statusCode)
		writeError(w, statusCode, "upstream error: "+err.Error(), "upstream_error")
		h.emitLog(r.Context(), tokenID, req.Model, route, nil, duration, statusCode, true, h.clientIP(r))
		return
	}

	h.router.RecordSuccessLatency(route.Channel.ID, duration)

	// Agentic loop: if response has MCP tool calls, dispatch and re-invoke.
	if h.mcpLoop != nil && resp != nil && len(resp.Choices) > 0 && len(resp.Choices[0].Message.ToolCalls) > 0 {
		newResp, mcpUsage, loopErr := h.mcpLoop.Execute(r.Context(), &req, route.KeyValue, route.Channel.BaseURL)
		if loopErr != nil {
			writeError(w, http.StatusInternalServerError, "agentic loop: "+loopErr.Error(), "agentic_error")
			h.emitLog(r.Context(), tokenID, req.Model, route, nil, duration, statusCode, true, h.clientIP(r))
			return
		}
		resp = newResp
		if len(mcpUsage.Calls) > 0 {
			h.emitter.EmitMCP(r.Context(), tokenID, route, mcpUsage, h.clientIP(r))
		}
	}

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
				costUSD := 0.0
				if mc, ok := h.modelCostFor(req.Model); ok {
					costUSD = h.costCalc.RealCost(mc, resp.Usage)
				}
				entry := &cache.Entry{
					Key:        ckey,
					StatusCode: http.StatusOK,
					Body:       mustMarshalJSON(resp),
					Usage:      &resp.Usage,
					CostUSD:    costUSD,
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
// to pick one channel, serial tries each underlying model in order,
// auto classifies the prompt into a complexity tier and samples the
// tier's (tier, model) quality arms.
func (h *Handler) handleCombo(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest, combo model.TokenComboModel, info middleware.TokenInfo) {
	if req.Stream {
		h.handleStreamCombo(w, r, req, combo)
		return
	}
	switch combo.Mode {
	case model.ComboModeSerial:
		h.handleSerialCombo(w, r, req, combo, info)
	case model.ComboModeAuto:
		h.handleAutoCombo(w, r, req, combo)
	default:
		h.handleLoadBalanceCombo(w, r, req, combo, info)
	}
}

// handleStreamCombo routes a streaming combo-model request. For
// load_balance it uses the L1-L5 pipeline with the combo's model pool;
// for serial it tries each model in order, streaming from the first
// that yields an upstream SSE channel; for auto it classifies the
// prompt, samples a tier arm, and streams from the first candidate
// that yields an SSE channel.
func (h *Handler) handleStreamCombo(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest, combo model.TokenComboModel) {
	switch combo.Mode {
	case model.ComboModeSerial:
		h.handleStreamSerialCombo(w, r, req, combo)
		return
	case model.ComboModeAuto:
		h.handleStreamAutoCombo(w, r, req, combo)
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
	// sees a model name it recognises (the combo name - e.g. "auto"
	// - is only meaningful to llmRx).
	originalModel := req.Model
	actualModel := originalModel
	if len(route.Channel.Models) > 0 {
		actualModel = route.Channel.Models[0]
		req.Model = actualModel
	}

	// Probe-based filtering now happens in L2 (CircuitBreaker.Filter)
	// for all requests, not just auto. The per-channel probe cache
	// is wired via RouterEngine.SetHealthChecker at startup.

	// Fail-closed: never call upstream for an unpriced model.
	if !h.requirePriced(w, actualModel) {
		return
	}
	prov := h.providerFor(route.Channel.Protocol, false)
	start := time.Now()
	resp, statusCode, err := prov.Chat(r.Context(), req, route.KeyValue, route.Channel.BaseURL)
	duration := time.Since(start).Milliseconds()

	req.Model = originalModel

	if err != nil {
		h.router.RecordFailure(route.Channel.ID, statusCode)
		observability.RecordUpstreamError(originalModel, statusCode)
		logging.Warn("combo.upstream_error",
			logging.F("model_requested", originalModel),
			logging.F("model_actual", req.Model),
			logging.F("channel", route.Channel.Name),
			logging.F("status", statusCode),
			logging.F("error", err.Error()),
		)
		writeError(w, statusCode, "upstream error: "+err.Error(), "upstream_error")
		h.emitLog(r.Context(), tokenID, actualModel, route, nil, duration, statusCode, true, h.clientIP(r))
		return
	}

	h.router.RecordSuccessLatency(route.Channel.ID, duration)
	logging.Debug("combo.upstream_success",
		logging.F("model_requested", originalModel),
		logging.F("model_actual", actualModel),
		logging.F("channel", route.Channel.Name),
		logging.F("status", statusCode),
		logging.F("duration_ms", duration),
	)
	h.emitLog(r.Context(), tokenID, actualModel, route, &resp.Usage, duration, statusCode, false, h.clientIP(r))
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

		// Fail-closed: never call upstream for an unpriced model.
		if !h.requirePriced(w, modelName) {
			return
		}
		// Attempt the actual chat.
		prov := h.providerFor(route.Channel.Protocol, false)
		start := time.Now()
		resp, statusCode, err := prov.Chat(r.Context(), req, route.KeyValue, route.Channel.BaseURL)
		duration := time.Since(start).Milliseconds()

		if err != nil || statusCode >= 500 {
			h.router.RecordFailure(route.Channel.ID, statusCode)
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
		h.router.RecordSuccessLatency(route.Channel.ID, duration)
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

// continuationModel returns the model this session last used when
// long-context continuation is enabled and the estimated context is
// large enough. Empty string disables continuation for this request.
func (h *Handler) continuationModel(sessionID string, estTokens int) string {
	if !h.continuationOn || sessionID == "" || estTokens < h.contThreshold {
		return ""
	}
	h.contMu.Lock()
	defer h.contMu.Unlock()
	return h.contModel[sessionID]
}

// rememberContinuation records the model that served a session turn
// (only when continuation is enabled, to bound memory).
func (h *Handler) rememberContinuation(sessionID, model string) {
	if !h.continuationOn || sessionID == "" || model == "" {
		return
	}
	h.contMu.Lock()
	h.contModel[sessionID] = model
	h.contMu.Unlock()
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
//     context is cancelled when it elapses.
//   - stream_max_body_bytes soft cap on bytes written to the client;
//     when exceeded the stream terminates with a
//     "limit_exceeded" error frame and the
//     channel is recorded as a failure.
//
// A value of 0 disables the corresponding cap.
func (h *Handler) streamChatCompletions(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest) {
	// Fail-closed: unpriced models never run upstream (even streamed).
	if !h.requirePriced(w, req.Model) {
		return
	}
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
		ctx                       = r.Context()
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
		h.router.RecordFailure(route.Channel.ID, http.StatusBadGateway)
		observability.RecordUpstreamError(req.Model, http.StatusBadGateway)
		h.emitLog(r.Context(), lookupTokenID(r.Context(), h.store), req.Model, route, nil,
			time.Since(start).Milliseconds(), http.StatusBadGateway, true, h.clientIP(r))
		return
	}

	var (
		usage          *provider.Usage
		flushed        = 0
		bytesSent      = int64(0)
		contentBuilder strings.Builder
		hasToolCalls   bool
	)
	contentBuilder.Grow(4096)
	var streamBuf *bytes.Buffer
	if h.responseCache != nil {
		streamBuf = streamBufPool.Get().(*bytes.Buffer)
		streamBuf.Reset()
	}
	// toolCallAcc accumulates streaming delta.tool_calls keyed by
	// Index. When finish_reason with tool_calls is seen, the
	// accumulator is used to build the full assistant message.
	var toolCallAcc map[int]*toolCallAccum
	toolCallAcc = make(map[int]*toolCallAccum, 4)
	for {
		select {
		case <-ctx.Done():
			reason := "client disconnected"
			if ctx.Err() == context.DeadlineExceeded {
				reason = "stream timeout exceeded"
			}
			h.router.RecordFailure(route.Channel.ID, http.StatusGatewayTimeout)
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
				h.router.RecordFailure(route.Channel.ID, http.StatusBadGateway)
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
				// Accumulate delta.tool_calls for agentic loop bridge.
				for _, tc := range c.Delta.ToolCalls {
					if tc.Index == nil {
						continue
					}
					idx := *tc.Index
					acc, ok := toolCallAcc[idx]
					if !ok {
						acc = toolCallAccumPool.Get().(*toolCallAccum)
						acc.Index = idx
						acc.ID = ""
						acc.Name.Reset()
						acc.Arguments.Reset()
						toolCallAcc[idx] = acc
					}
					if tc.ID != "" {
						acc.ID = tc.ID
					}
					if tc.Function.Name != "" {
						acc.Name.WriteString(tc.Function.Name)
					}
					if tc.Function.Arguments != "" {
						acc.Arguments.WriteString(tc.Function.Arguments)
					}
				}
				if c.FinishReason == "tool_calls" {
					hasToolCalls = true
				}
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
			if streamBuf != nil {
				streamBuf.Write(chunkBytes)
			}
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
				h.router.RecordFailure(route.Channel.ID, http.StatusBadGateway)
				h.emitLog(r.Context(), lookupTokenID(r.Context(), h.store), req.Model, route, usage,
					time.Since(start).Milliseconds(), http.StatusRequestEntityTooLarge, true, h.clientIP(r))
				// Stop the upstream pump (same shape as the
				// w.Write-error branch below); without this the
				// sender blocks on the buffered-out channel.
				cancel()
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
	// If the stream produced tool_calls and we have an agentic loop,
	// enter the streaming bridge: emit event: agentic-loop, resolve
	// tools via ResolveTools, then issue a second StreamChat with the
	// accumulated messages and pipe the final answer.
	if hasToolCalls && h.mcpLoop != nil {
		// Build the full assistant message from accumulated deltas.
		assistantMsg := provider.Message{Role: "assistant"}
		indices := make([]int, 0, len(toolCallAcc))
		for idx := range toolCallAcc {
			indices = append(indices, idx)
		}
		sort.Ints(indices)
		for _, idx := range indices {
			acc := toolCallAcc[idx]
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, provider.ToolCall{
				ID:   acc.ID,
				Type: "function",
				Function: provider.FunctionCall{
					Name:      acc.Name.String(),
					Arguments: acc.Arguments.String(),
				},
			})
		}
		for _, acc := range toolCallAcc {
			toolCallAccumPool.Put(acc)
		}

		// Emit agentic-loop SSE event.
		agenticEvent := []byte("event: agentic-loop\ndata: {}\n\n")
		if streamBuf != nil {
			streamBuf.Write(agenticEvent)
		}
		w.Write(agenticEvent)
		flusher.Flush()

		// Append the assistant message to the request and resolve tools.
		req.Messages = append(req.Messages, assistantMsg)
		msgs, mcpUsage, loopErr := h.mcpLoop.ResolveTools(r.Context(), req, route.KeyValue, route.Channel.BaseURL)
		if loopErr != nil {
			h.router.RecordFailure(route.Channel.ID, http.StatusBadGateway)
			errMsg := fmt.Sprintf("event: error\ndata: {\"message\":%q}\n\n", loopErr.Error())
			if streamBuf != nil {
				streamBuf.WriteString(errMsg)
			}
			w.Write([]byte(errMsg))
			flusher.Flush()
			h.emitLog(r.Context(), lookupTokenID(r.Context(), h.store), req.Model, route, usage,
				time.Since(start).Milliseconds(), http.StatusInternalServerError, true, h.clientIP(r))
			return
		}
		if len(mcpUsage.Calls) > 0 {
			h.emitter.EmitMCP(r.Context(), lookupTokenID(r.Context(), h.store), route, mcpUsage, h.clientIP(r))
		}

		// Issue a second StreamChat with the accumulated messages.
		req.Messages = msgs
		ch2, err := sp.StreamChat(ctx, req, route.KeyValue, route.Channel.BaseURL)
		if err != nil {
			h.router.RecordFailure(route.Channel.ID, http.StatusBadGateway)
			errMsg := fmt.Sprintf("event: error\ndata: {\"message\":%q}\n\n", err.Error())
			if streamBuf != nil {
				streamBuf.WriteString(errMsg)
			}
			w.Write([]byte(errMsg))
			flusher.Flush()
			h.emitLog(r.Context(), lookupTokenID(r.Context(), h.store), req.Model, route, usage,
				time.Since(start).Milliseconds(), http.StatusBadGateway, true, h.clientIP(r))
			return
		}
		// Reset content builder for second-stream guardrail check.
		contentBuilder.Reset()
		for {
			select {
			case <-ctx.Done():
				reason := "client disconnected"
				if ctx.Err() == context.DeadlineExceeded {
					reason = "stream timeout exceeded"
				}
				h.router.RecordFailure(route.Channel.ID, http.StatusGatewayTimeout)
				fmt.Fprintf(w, "event: error\ndata: {\"message\":%q}\n\n", reason)
				flusher.Flush()
				h.emitLog(r.Context(), lookupTokenID(r.Context(), h.store), req.Model, route, usage,
					time.Since(start).Milliseconds(), http.StatusGatewayTimeout, true, h.clientIP(r))
				return
			case ev, ok := <-ch2:
				if !ok {
					goto final
				}
				if ev.Err != nil {
					h.router.RecordFailure(route.Channel.ID, http.StatusBadGateway)
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
				if b := buf.Bytes(); len(b) > 0 && b[len(b)-1] == '\n' {
					buf.WriteByte('\n')
				} else {
					buf.Write(dataSuffix)
				}
				chunkBytes := buf.Bytes()
				if streamBuf != nil {
					streamBuf.Write(chunkBytes)
				}
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
					h.router.RecordFailure(route.Channel.ID, http.StatusRequestEntityTooLarge)
					h.emitLog(r.Context(), lookupTokenID(r.Context(), h.store), req.Model, route, usage,
						time.Since(start).Milliseconds(), http.StatusRequestEntityTooLarge, true, h.clientIP(r))
					cancel()
					return
				}
				flushed++
				if flushed%4 == 0 {
					flusher.Flush()
				}
			}
		}
	}
	// If no tool calls or no agentic loop, proceed to guardrail + [DONE].
final:
	if h.guardrails != nil {
		tokenID := lookupTokenID(r.Context(), h.store)
		if gr := h.guardrails.CheckOutput(r.Context(), contentBuilder.String(), tokenID); gr != nil {
			fmt.Fprintf(w, "event: error\ndata: {\"message\":%q}\n\n", gr.Message)
			flusher.Flush()
			h.emitLog(r.Context(), tokenID, req.Model, route, usage,
				time.Since(start).Milliseconds(), http.StatusOK, true, h.clientIP(r))
			// Upstream pump must stop too, or it blocks on out.
			cancel()
			return
		}
	}
	if streamBuf != nil {
		streamBuf.Write(donePayload)
	}
	w.Write(donePayload)
	flusher.Flush()
	h.router.RecordSuccessLatency(route.Channel.ID, time.Since(start).Milliseconds())
	if streamBuf != nil {
		body := streamBuf.Bytes()
		bodyCopy := make([]byte, len(body))
		copy(bodyCopy, body)
		putStreamBuf(streamBuf)
		h.tryCacheStream(r.Context(), req, route, usage, bodyCopy)
	}
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
		if mc, ok := h.modelCostFor(req.Model); ok {
			costUSD = h.costCalc.RealCost(mc, *usage)
		}
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
