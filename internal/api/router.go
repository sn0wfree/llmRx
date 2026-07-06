package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/router"
)

type Handler struct {
	router    *router.RouterEngine
	pool      *pool.ChannelPool
	provider  provider.Provider
	cfg       *config.Config
	defaultMarkup float64
}

// New wires the HTTP handler. P0 uses a single OpenAI-compatible
// provider for every channel; channel.Provider is only carried for
// /v1/models display and future multi-protocol adapters.
func New(cfg *config.Config, eng *router.RouterEngine, cp *pool.ChannelPool) *Handler {
	markup := 1.0
	if cfg.Server.RateLimit <= 0 {
		// intentionally unused in P0; reserved for per-plan override
	}
	return &Handler{
		router:        eng,
		pool:          cp,
		provider:      provider.NewOpenAIProvider(),
		cfg:           cfg,
		defaultMarkup: markup,
	}
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
		writeError(w, http.StatusNotImplemented, "streaming is not supported yet", "stream_unsupported")
		return
	}

	route, err := h.router.Route(context.Background(), req.Model)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "no available channel: "+err.Error(), "no_channel")
		return
	}

	start := time.Now()
	resp, statusCode, err := h.provider.Chat(&req, route.KeyValue, route.Channel.BaseURL)
	duration := time.Since(start).Milliseconds()

	if err != nil {
		h.router.RecordFailure(route.Channel.ID)
		writeError(w, statusCode, "upstream error: "+err.Error(), "upstream_error")
		h.emitLog(req.Model, route, nil, duration, statusCode, true)
		return
	}

	h.router.RecordSuccess(route.Channel.ID)
	h.emitLog(req.Model, route, &resp.Usage, duration, statusCode, false)
	writeJSON(w, resp)
}

func (h *Handler) emitLog(modelName string, route *router.RouteResult, usage *provider.Usage, durationMs int64, statusCode int, failed bool) {
	real := 0.0
	if usage != nil {
		real = calcCost(route.Channel, *usage)
	}
	entry := model.Log{
		TokenID:         0,
		ChannelID:       route.Channel.ID,
		KeyID:           route.Key.ID,
		Model:           modelName,
		PromptTokens:    intField(usage, "prompt"),
		CompletionTokens: intField(usage, "completion"),
		RealCostUSD:     real,
		BilledCostUSD:   real * h.defaultMarkup,
		DurationMs:      durationMs,
		StatusCode:      statusCode,
		RouterPath:      route.RouterLog,
		CreatedAt:       time.Now(),
	}
	status := "ok"
	if failed {
		status = "fail"
	}
	log.Printf("log status=%s model=%s channel=%s key=%s prompt=%d completion=%d real_usd=%.6f billed_usd=%.6f duration_ms=%d code=%d path=%s",
		status, entry.Model, route.Channel.Name, route.Key.KeyMasked,
		entry.PromptTokens, entry.CompletionTokens,
		entry.RealCostUSD, entry.BilledCostUSD,
		entry.DurationMs, entry.StatusCode, entry.RouterPath,
	)
}

func intField(u *provider.Usage, which string) int {
	if u == nil {
		return 0
	}
	if which == "prompt" {
		return u.PromptTokens
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