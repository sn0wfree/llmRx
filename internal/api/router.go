package api

import (
	"context"
	"encoding/json"
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
	providers map[string]provider.Provider
	cfg       *config.Config
}

func New(eng *router.RouterEngine, cp *pool.ChannelPool) *Handler {
	return &Handler{
		router: eng,
		pool:   cp,
		providers: map[string]provider.Provider{
			"openai": provider.NewOpenAIProvider(),
		},
	}
}

type errorResp struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := errorResp{}
	resp.Error.Message = msg
	resp.Error.Type = "api_error"
	json.NewEncoder(w).Encode(resp)
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req provider.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}

	route, err := h.router.Route(context.Background(), req.Model)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "no available channel: "+err.Error())
		return
	}

	p, ok := h.providers["openai"]
	if !ok {
		writeError(w, http.StatusInternalServerError, "provider not found")
		return
	}

	start := time.Now()
	resp, statusCode, err := p.Chat(&req, route.KeyValue, route.Channel.BaseURL)
	duration := time.Since(start).Milliseconds()

	if err != nil {
		h.router.RecordFailure(route.Channel.ID)
		writeError(w, statusCode, "upstream error: "+err.Error())
		return
	}

	h.router.RecordSuccess(route.Channel.ID)

	logEntry := &model.Log{
		ChannelID:       route.Channel.ID,
		KeyID:           route.Key.ID,
		Model:           req.Model,
		PromptTokens:    resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		RealCostUSD:     calcCost(route.Channel, resp.Usage),
		BilledCostUSD:   calcCost(route.Channel, resp.Usage),
		DurationMs:      duration,
		StatusCode:      statusCode,
		RouterPath:      route.RouterLog,
		CreatedAt:       time.Now(),
	}
	_ = logEntry

	writeJSON(w, resp)
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
