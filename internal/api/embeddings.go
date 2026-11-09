package api

import (
	"context"
	"time"

	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/observability"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/router"
	"net/http"
)

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
			logging.Warn("api.model_denied", logging.F("model", req.Model), logging.F("token_id", info.ID), logging.F("endpoint", "embeddings"))
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
		h.router.RecordFailure(route.Channel.ID, statusCode)
		observability.RecordUpstreamError(req.Model, statusCode)
		writeError(w, statusCode, "upstream error: "+err.Error(), "upstream_error")
		h.emitLog(r.Context(), tokenID, req.Model, route, nil, duration, statusCode, true, h.clientIP(r))
		return
	}

	h.router.RecordSuccessLatency(route.Channel.ID, duration)
	h.emitLog(r.Context(), tokenID, req.Model, route, &resp.Usage, duration, statusCode, false, h.clientIP(r))
	writeJSON(w, resp)
}
