package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/router/auto"
	"github.com/sn0wfree/llmRx/internal/router/session"
	"net/http"
)

// handleAutoCombo routes a mode:auto combo request. It classifies
// the last user message into a complexity tier, samples the tier's
// (tier, model) quality arms via Thompson sampling, then tries
// models in order — the sampled arm first, the remaining tier
// candidates in cost order, then the combo's Fallback list — until
// a 2xx arrives. Every actual upstream call updates both the
// channel-level L5 posterior and the (tier, model) arm, so the
// router learns which model really succeeds per tier.
func (h *Handler) handleAutoCombo(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest, combo model.TokenComboModel) {
	tokenID := lookupTokenID(r.Context(), h.store)
	text := lastUserText(req.Messages)

	// Session affinity: a live pin short-circuits classification and
	// arm sampling, routing straight to the model that served the
	// first turn (preserves upstream prompt caches). A failed or
	// unroutable pinned model drops the pin and re-classifies.
	sessionID := h.sessionIDFor(r, req)
	if h.sessions != nil {
		if pin, ok := h.sessions.Get(sessionID); ok && pin.Model != "" {
			if h.autoAttemptPinned(w, r, req, combo, pin, tokenID, text) {
				return
			}
			// Pinned model failed: drop the pin, fall through to a
			// fresh decision.
			h.sessions.Delete(sessionID)
		}
	}

	sc := h.autoScorer.Classify(text)
	tier := string(sc.Tier)

	// Task-aware routing: resolve the task type and the session's
	// continuation model before building the attempt order.
	task, taskSource := h.resolveTask(r, req, text)
	est := auto.TokenEstimate(lastUserText(req.Messages))
	for _, m := range req.Messages {
		est += auto.TokenEstimate(m.ContentString())
	}
	contModel := h.continuationModel(sessionID, est)

	decision := &auto.Decision{
		Tier: tier, Score: sc.Score, Cause: sc.Cause,
		Task: task, TaskSource: taskSource, EstTokens: est,
		Continuation: contModel != "",
	}
	order, escalated := h.autoAttemptOrder(combo, tier, text, &decision.Picked, task, contModel)
	decision.ContextEscalation = escalated
	if escalated {
		decision.Candidates = combo.ContextFallback
	} else {
		decision.Candidates = tierCandidates(combo, tier)
	}

	opts := router.RouteOptions{Text: text}
	var lastErr error
	for _, m := range order {
		// Safety net: fallback attempts bypass the circuit breaker —
		// a degraded call beats a hard failure.
		opts.SkipBreaker = !isTierCandidate(combo, tier, m)
		route, routeErr := h.router.RouteWith(context.Background(), m, opts)
		if routeErr != nil {
			lastErr = routeErr
			continue
		}
		decision.Attempted = append(decision.Attempted, m)

		// Fail-closed: never call upstream for an unpriced model.
		if !h.requirePriced(w, m) {
			return
		}
		prov := h.providerFor(route.Channel.Protocol, false)
		start := time.Now()
		resp, statusCode, err := prov.Chat(r.Context(), req, route.KeyValue, route.Channel.BaseURL)
		duration := time.Since(start).Milliseconds()

		if err != nil || statusCode >= 500 {
			h.router.RecordFailure(route.Channel.ID, statusCode)
			if isTierCandidate(combo, tier, m) {
				h.router.RecordArmFailure(auto.ArmKey(tier, m))
			}
			lastErr = fmt.Errorf("model %s: status=%d err=%w", m, statusCode, err)
			logging.Debug("auto.attempt_failed",
				logging.F("combo", combo.Name),
				logging.F("tier", tier),
				logging.F("model", m),
				logging.F("channel", route.Channel.Name),
				logging.F("status", statusCode),
				logging.F("error", err),
			)
			h.emitLog(r.Context(), tokenID, m, route, nil, duration, statusCode, true, h.clientIP(r))
			continue
		}

		h.router.RecordSuccessLatency(route.Channel.ID, duration)
		if isTierCandidate(combo, tier, m) {
			h.router.RecordArmSuccess(auto.ArmKey(tier, m))
		}
		decision.Routed = m
		decision.Fallback = !isTierCandidate(combo, tier, m)
		if h.sessions != nil {
			h.sessions.Set(sessionID, m, tier, route.Channel.ID)
		}
		h.rememberContinuation(sessionID, m)
		h.emitAutoDecision(r, decision)
		h.setAutoHeaders(w, decision)
		logging.Debug("auto.success",
			logging.F("combo", combo.Name),
			logging.F("tier", tier),
			logging.F("model", m),
			logging.F("channel", route.Channel.Name),
			logging.F("status", statusCode),
			logging.F("duration_ms", duration),
		)
		h.emitLog(r.Context(), tokenID, m, route, &resp.Usage, duration, statusCode, false, h.clientIP(r))
		writeJSON(w, resp)
		return
	}

	// Every candidate and fallback failed.
	decision.Routed = ""
	h.emitAutoDecision(r, decision)
	h.setAutoHeaders(w, decision)
	if lastErr != nil {
		writeError(w, http.StatusBadGateway, "all auto combo models failed: "+lastErr.Error(), "combo_all_failed")
	} else {
		writeError(w, http.StatusServiceUnavailable, "no channel matched for auto combo models", "no_channel")
	}
}

// autoAttemptPinned routes one request to the pinned session model.
// It returns true when the request was fully handled (success OR
// hard failure with a response written). A routing failure or an
// upstream 5xx leaves the pin in place for the caller to drop and
// re-classify — but the caller only reaches here when the pin was
// live, so a 5xx means the channel is flaky: the caller deletes the
// pin and falls through.
func (h *Handler) autoAttemptPinned(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest, combo model.TokenComboModel, pin session.Pin, tokenID int64, text string) bool {
	opts := router.RouteOptions{Text: text}
	// The pinned model may be a fallback/context model not in the
	// current tier — always bypass the breaker for a pinned retry:
	// the pin existed because this channel served the conversation.
	opts.SkipBreaker = true
	route, routeErr := h.router.RouteWith(context.Background(), pin.Model, opts)
	if routeErr != nil {
		logging.Debug("auto.pin_unroutable",
			logging.F("model", pin.Model),
			logging.F("error", routeErr),
		)
		return false
	}
	// Fail-closed: never call upstream for an unpriced model.
	if !h.requirePriced(w, pin.Model) {
		return true
	}
	prov := h.providerFor(route.Channel.Protocol, false)
	start := time.Now()
	resp, statusCode, err := prov.Chat(r.Context(), req, route.KeyValue, route.Channel.BaseURL)
	duration := time.Since(start).Milliseconds()
	if err != nil || statusCode >= 500 {
		h.router.RecordFailure(route.Channel.ID, statusCode)
		logging.Debug("auto.pin_failed",
			logging.F("model", pin.Model),
			logging.F("channel", route.Channel.Name),
			logging.F("status", statusCode),
			logging.F("error", err),
		)
		h.emitLog(r.Context(), tokenID, pin.Model, route, nil, duration, statusCode, true, h.clientIP(r))
		return false
	}
	h.router.RecordSuccessLatency(route.Channel.ID, duration)
	if h.sessions != nil {
		h.sessions.Set(sessionIDFromReq(req), pin.Model, pin.Tier, route.Channel.ID)
	}
	w.Header().Set("X-llmRx-Session-Pinned", "true")
	w.Header().Set("X-llmRx-Routed-Model", pin.Model)
	w.Header().Set("X-llmRx-Auto-Tier", pin.Tier)
	logging.Debug("auto.pin_hit",
		logging.F("model", pin.Model),
		logging.F("channel", route.Channel.Name),
		logging.F("status", statusCode),
		logging.F("duration_ms", duration),
	)
	h.emitLog(r.Context(), tokenID, pin.Model, route, &resp.Usage, duration, statusCode, false, h.clientIP(r))
	writeJSON(w, resp)
	return true
}

// handleStreamAutoCombo routes a streaming mode:auto combo request.
// Classification and arm sampling are identical to the non-streaming
// path; the first model that yields an upstream SSE channel streams
// the response. Channel-level L5 updates happen inside
// streamChatCompletions; arm-level updates for streaming land with
// the execution-layer work.
func (h *Handler) handleStreamAutoCombo(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest, combo model.TokenComboModel) {
	text := lastUserText(req.Messages)

	// Session affinity: a live pin routes straight to the pinned
	// model without re-classifying. Stream failures drop the pin so
	// the next turn re-decides.
	sessionID := h.sessionIDFor(r, req)
	if h.sessions != nil {
		if pin, ok := h.sessions.Get(sessionID); ok && pin.Model != "" {
			if h.streamAttemptPinned(w, r, req, combo, pin) {
				return
			}
			h.sessions.Delete(sessionID)
		}
	}

	sc := h.autoScorer.Classify(text)
	tier := string(sc.Tier)

	task, taskSource := h.resolveTask(r, req, text)
	est := auto.TokenEstimate(text)
	for _, m := range req.Messages {
		est += auto.TokenEstimate(m.ContentString())
	}
	contModel := h.continuationModel(sessionID, est)

	decision := &auto.Decision{
		Tier: tier, Score: sc.Score, Cause: sc.Cause,
		Task: task, TaskSource: taskSource, EstTokens: est,
		Continuation: contModel != "",
	}
	order, escalated := h.autoAttemptOrder(combo, tier, text, &decision.Picked, task, contModel)
	decision.ContextEscalation = escalated
	if escalated {
		decision.Candidates = combo.ContextFallback
	} else {
		decision.Candidates = tierCandidates(combo, tier)
	}

	opts := router.RouteOptions{Text: text}
	var lastErr error
	for _, m := range order {
		opts.SkipBreaker = !isTierCandidate(combo, tier, m)
		route, routeErr := h.router.RouteWith(r.Context(), m, opts)
		if routeErr != nil {
			lastErr = routeErr
			continue
		}
		prov := h.providerFor(route.Channel.Protocol, true)
		if _, ok := prov.(provider.StreamingProvider); !ok {
			lastErr = fmt.Errorf("model %s: streaming not supported", m)
			continue
		}
		decision.Attempted = append(decision.Attempted, m)
		decision.Routed = m
		decision.Fallback = !isTierCandidate(combo, tier, m)
		if h.sessions != nil {
			h.sessions.Set(sessionID, m, tier, route.Channel.ID)
		}
		h.rememberContinuation(sessionID, m)
		h.emitAutoDecision(r, decision)
		h.setAutoHeaders(w, decision)
		logging.Debug("combo.stream_auto.success",
			logging.F("combo", combo.Name),
			logging.F("tier", tier),
			logging.F("model", m),
			logging.F("channel", route.Channel.Name),
		)
		original := req.Model
		req.Model = m
		h.streamChatCompletions(w, r, req)
		req.Model = original
		return
	}

	decision.Routed = ""
	h.emitAutoDecision(r, decision)
	h.setAutoHeaders(w, decision)
	if lastErr != nil {
		writeError(w, http.StatusBadGateway, "all auto combo models failed: "+lastErr.Error(), "combo_all_failed")
	} else {
		writeError(w, http.StatusServiceUnavailable, "no channel matched for auto combo models", "no_channel")
	}
}

// streamAttemptPinned streams one request through the pinned model.
// Returns true when the request was fully handled. Any failure —
// unroutable model, non-streaming channel, upstream error — is
// reported by the caller deleting the pin and re-deciding.
func (h *Handler) streamAttemptPinned(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest, combo model.TokenComboModel, pin session.Pin) bool {
	opts := router.RouteOptions{Text: lastUserText(req.Messages)}
	opts.SkipBreaker = true
	route, routeErr := h.router.RouteWith(r.Context(), pin.Model, opts)
	if routeErr != nil {
		return false
	}
	prov := h.providerFor(route.Channel.Protocol, true)
	if _, ok := prov.(provider.StreamingProvider); !ok {
		return false
	}
	w.Header().Set("X-llmRx-Session-Pinned", "true")
	w.Header().Set("X-llmRx-Auto-Tier", pin.Tier)
	logging.Debug("combo.stream_auto.pin_hit",
		logging.F("model", pin.Model),
		logging.F("channel", route.Channel.Name),
	)
	original := req.Model
	req.Model = pin.Model
	h.streamChatCompletions(w, r, req)
	req.Model = original
	return true
}

// autoAttemptOrder builds the attempt order for one auto request:
// the sampled arm first (highest θ draw, or the cheapest candidate
// while the cold-start gate is on), then the remaining tier
// candidates in cost order, then the combo's Fallback list. A
// fallback model already present in the tier candidates is not
// retried a second time. Tier candidates whose context window
// cannot fit the prompt are filtered out before sampling.
//
// Task-aware upgrades:
//   - task ("" disables): tier candidates are reordered so the
//     task_model_bias preference for this task comes first (affects
//     the cold-start gate and the non-sampled attempt order).
//   - contModel ("" disables): session continuation moves the model
//     this long-context session already used to the front of the
//     attempt order — tool-chain continuity beats a per-turn pick.
//
// When every tier candidate has a context window too small for the
// prompt and the combo declares a ContextFallback list, the order
// escalates: models are drawn from ContextFallback sorted by
// context window ascending (smallest that fits first) and the
// returned escalated flag tells the caller to log the decision as
// context_escalation. Escalation skips arm sampling — the
// context_fallback models are a safety net, not quality arms.
func (h *Handler) autoAttemptOrder(combo model.TokenComboModel, tier, text string, picked *auto.ArmSample, task, contModel string) ([]string, bool) {
	est := auto.TokenEstimate(text)
	raw := tierCandidates(combo, tier)
	escalated := auto.AllContextFiltered(raw, est) && len(combo.ContextFallback) > 0
	var candidates []string
	if escalated {
		candidates = auto.SelectContextFallback(combo.ContextFallback, est)
	} else {
		candidates = auto.FilterContextCandidates(raw, est)
		// Task preference reorders candidates before sampling
		// (affects the cold-start gate and fallback order).
		if task != "" {
			candidates, _ = auto.ReorderByTask(candidates, task, h.taskModelBias)
		}
	}
	*picked = h.autoPool.Select(tier, candidates)
	order := make([]string, 0, len(candidates)+len(combo.Fallback))
	if picked.Model != "" {
		order = append(order, picked.Model)
	}
	for _, m := range candidates {
		if m != picked.Model {
			order = append(order, m)
		}
	}
	// Session continuation: prefer the model the session already
	// uses (long-context agentic sessions). Move it to the front of
	// the attempt order; if it fails, the rest still apply.
	if contModel != "" {
		order, _ = auto.MoveToFront(order, contModel)
	}
	for _, m := range combo.Fallback {
		if !containsString(order, m) {
			order = append(order, m)
		}
	}
	return order, escalated
}

// tierCandidates returns the tier's model list in cost order, or
// nil when the combo has no entry for the tier (the fallback list
// then takes over). The slice is the combo's backing storage —
// callers must not mutate it (they don't: it's read for sampling,
// context-budget filtering, and the response decision header).
func tierCandidates(combo model.TokenComboModel, tier string) []string {
	cfg, ok := combo.Tiers[tier]
	if !ok || len(cfg.Models) == 0 {
		return nil
	}
	return cfg.Models
}

// isTierCandidate reports whether m belongs to the tier's candidate
// list. Fallback models are not tier candidates and therefore do
// not update (tier, model) quality arms.
func isTierCandidate(combo model.TokenComboModel, tier, m string) bool {
	for _, c := range tierCandidates(combo, tier) {
		if c == m {
			return true
		}
	}
	return false
}

// emitAutoDecision writes the per-request decision line so every
// auto-routed call is auditable:
// auto_tier=complex cause=heuristic score=0.71 arm=complex:gpt-4o θ=0.62 routed=deepseek-chat fallback=false attempted=...
// and folds the decision into the stats accumulator for the admin
// state endpoint.
func (h *Handler) emitAutoDecision(r *http.Request, d *auto.Decision) {
	h.autoStats.Record(*d)
	logging.Debug("auto.decision",
		logging.F("auto_tier", d.Tier),
		logging.F("cause", d.Cause),
		logging.F("score", fmt.Sprintf("%.3f", d.Score)),
		logging.F("arm", d.Picked.Key),
		logging.F("theta", fmt.Sprintf("%.3f", d.Picked.Score)),
		logging.F("routed", d.Routed),
		logging.F("fallback", d.Fallback),
		logging.F("context_escalation", d.ContextEscalation),
		logging.F("attempted", strings.Join(d.Attempted, ",")),
	)
}

// setAutoHeaders stamps the decision onto the response so callers
// can see which tier and model served them.
func (h *Handler) setAutoHeaders(w http.ResponseWriter, d *auto.Decision) {
	w.Header().Set("X-llmRx-Auto-Tier", d.Tier)
	if d.Routed != "" {
		w.Header().Set("X-llmRx-Routed-Model", d.Routed)
	}
}
