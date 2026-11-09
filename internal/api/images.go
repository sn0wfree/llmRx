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
			logging.Warn("api.model_denied", logging.F("model", req.Model), logging.F("token_id", info.ID), logging.F("endpoint", "images/generations"))
			writeError(w, http.StatusForbidden, "model not allowed for this token", "model_not_allowed")
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
		h.router.RecordFailure(route.Channel.ID, statusCode)
		observability.RecordUpstreamError(req.Model, statusCode)
		writeError(w, statusCode, "upstream error: "+err.Error(), "upstream_error")
		h.emitLog(r.Context(), tokenID, req.Model, route, nil, duration, statusCode, true, h.clientIP(r))
		return
	}
	h.router.RecordSuccessLatency(route.Channel.ID, duration)
	var usage *provider.Usage
	if resp.Usage != nil {
		usage = resp.Usage
	}
	h.emitLog(r.Context(), tokenID, req.Model, route, usage, duration, statusCode, false, h.clientIP(r))
	writeJSON(w, resp)
}
