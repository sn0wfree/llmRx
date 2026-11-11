package api

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/observability"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/router"
)

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
			logging.Warn("api.model_denied", logging.F("model", req.Model), logging.F("token_id", info.ID), logging.F("endpoint", "audio/speech"))
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
		h.router.RecordFailure(route.Channel.ID, statusCode)
		observability.RecordUpstreamError(req.Model, statusCode)
		writeError(w, statusCode, "upstream error: "+err.Error(), "upstream_error")
		h.emitLog(r.Context(), tokenID, req.Model, route, nil, duration, statusCode, true, h.clientIP(r))
		return
	}
	h.router.RecordSuccessLatency(route.Channel.ID, duration)
	h.emitLog(r.Context(), tokenID, req.Model, route, nil, duration, statusCode, false, h.clientIP(r))
	format := req.ResponseFormat
	if format == "" {
		format = "mp3"
	}
	ct := "audio/" + format
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.Itoa(len(audio)))
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
			logging.Warn("api.model_denied", logging.F("model", req.Model), logging.F("token_id", info.ID), logging.F("endpoint", "audio/transcriptions"))
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
		h.router.RecordFailure(route.Channel.ID, statusCode)
		observability.RecordUpstreamError(req.Model, statusCode)
		writeError(w, statusCode, "upstream error: "+err.Error(), "upstream_error")
		h.emitLog(r.Context(), tokenID, req.Model, route, nil, duration, statusCode, true, h.clientIP(r))
		return
	}
	h.router.RecordSuccessLatency(route.Channel.ID, duration)
	var usage *provider.Usage
	if resp.Duration > 0 {
		usage = &provider.Usage{TotalTokens: int(resp.Duration * 1000)}
	}
	h.emitLog(r.Context(), tokenID, req.Model, route, usage, duration, statusCode, false, h.clientIP(r))
	writeJSON(w, resp)
}
