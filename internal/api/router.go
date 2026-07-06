package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sn0wfree/llmRx/internal/broker"
	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/provider"
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
	}
}

// providerFor returns the provider matching channel.Protocol,
// falling back to the default OpenAI provider if unknown.
func (h *Handler) providerFor(channelProtocol string) provider.Provider {
	if p, ok := h.providers[channelProtocol]; ok {
		return p
	}
	return h.provider
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

	if req.Stream {
		h.streamChatCompletions(w, r, &req)
		return
	}

	route, err := h.router.RouteWith(context.Background(), req.Model, router.RouteOptions{Text: lastUserText(req.Messages)})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "no available channel: "+err.Error(), "no_channel")
		return
	}

	prov := h.providerFor(route.Channel.Protocol)
	start := time.Now()
	resp, statusCode, err := prov.Chat(&req, route.KeyValue, route.Channel.BaseURL)
	duration := time.Since(start).Milliseconds()

	tokenID := lookupTokenID(r.Context(), h.store)

	if err != nil {
		h.router.RecordFailure(route.Channel.ID)
		writeError(w, statusCode, "upstream error: "+err.Error(), "upstream_error")
		h.emitLog(tokenID, req.Model, route, nil, duration, statusCode, true, clientIP(r))
		return
	}

	h.router.RecordSuccess(route.Channel.ID)
	h.emitLog(tokenID, req.Model, route, &resp.Usage, duration, statusCode, false, clientIP(r))
	writeJSON(w, resp)
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		return v
	}
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return v
	}
	return r.RemoteAddr
}

func lookupTokenID(ctx context.Context, st store.Store) int64 {
	v, ok := ctx.Value(middleware.TokenIDKey).(int64)
	if !ok {
		return 0
	}
	return v
}

func (h *Handler) emitLog(tokenID int64, modelName string, route *router.RouteResult, usage *provider.Usage, durationMs int64, statusCode int, failed bool, ip string) {
	real := 0.0
	if usage != nil {
		real = calcCost(route.Channel, *usage)
	}
	status := "ok"
	if failed {
		status = "fail"
	}
	log.Printf("log status=%s model=%s channel=%s key=%s prompt=%d completion=%d real_usd=%.6f billed_usd=%.6f duration_ms=%d code=%d path=%s",
		status, modelName, route.Channel.Name, route.Key.KeyMasked,
		promptTokens(usage), completionTokens(usage),
		real, real*h.Markup(),
		durationMs, statusCode, route.RouterLog,
	)
	entry := &model.Log{
		TokenID:         tokenID,
		ChannelID:       route.Channel.ID,
		KeyID:           route.Key.ID,
		Model:           modelName,
		PromptTokens:    promptTokens(usage),
		CompletionTokens: completionTokens(usage),
		RealCostUSD:     real,
		BilledCostUSD:   real * h.Markup(),
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

func calcCost(ch *model.Channel, usage provider.Usage) float64 {
	input := (float64(usage.PromptTokens) / 1000000.0) * ch.InputPrice
	output := (float64(usage.CompletionTokens) / 1000000.0) * ch.OutputPrice
	return input + output
}


// streamChatCompletions is invoked when the client sets stream=true.
// It performs the normal route selection, asks the upstream for a
// stream, and writes each chunk back as an SSE event. The
// Content-Type is text/event-stream and the response is flushed
// after every chunk so the client sees tokens as they're produced.
func (h *Handler) streamChatCompletions(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest) {
	route, err := h.router.RouteWith(r.Context(), req.Model, router.RouteOptions{Text: lastUserText(req.Messages)})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "no available channel: "+err.Error(), "no_channel")
		return
	}
	prov := h.providerFor(route.Channel.Protocol)
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
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	ch, err := sp.StreamChat(ctx, req, route.KeyValue, route.Channel.BaseURL)
	if err != nil {
		// Emit a single error frame and bail.
		fmt.Fprintf(w, "event: error\ndata: {\"message\":%q}\n\n", err.Error())
		flusher.Flush()
		h.router.RecordFailure(route.Channel.ID)
		h.emitLog(lookupTokenID(r.Context(), h.store), req.Model, route, nil,
			time.Since(start).Milliseconds(), http.StatusBadGateway, true, clientIP(r))
		return
	}

	var usage *provider.Usage
	flushed := 0
	for ev := range ch {
		if ev.Err != nil {
			h.router.RecordFailure(route.Channel.ID)
			fmt.Fprintf(w, "event: error\ndata: {\"message\":%q}\n\n", ev.Err.Error())
			flusher.Flush()
			h.emitLog(lookupTokenID(r.Context(), h.store), req.Model, route, usage,
				time.Since(start).Milliseconds(), http.StatusBadGateway, true, clientIP(r))
			return
		}
		if ev.Chunk.Usage != nil {
			usage = ev.Chunk.Usage
		}
		payload, err := json.Marshal(ev.Chunk)
		if err != nil {
			continue
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
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
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
	h.router.RecordSuccess(route.Channel.ID)
	h.emitLog(lookupTokenID(r.Context(), h.store), req.Model, route, usage,
		time.Since(start).Milliseconds(), http.StatusOK, false, clientIP(r))
}

// lastUserText returns the last user-role message in the conversation
// for L4 intent classification. If no user message is present, the
// empty string is returned, which disables L4.
func lastUserText(msgs []provider.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}
