package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/model"
)

// ListGuardrailRules returns all configured guardrail rules.
func (h *Handler) ListGuardrailRules(w http.ResponseWriter, r *http.Request) {
	rules, err := h.store.GetGuardrailRules()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rules == nil {
		rules = []model.GuardrailRule{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

// CreateGuardrailRule creates a new guardrail rule and reloads the engine.
func (h *Handler) CreateGuardrailRule(w http.ResponseWriter, r *http.Request) {
	var rule model.GuardrailRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if rule.Name == "" || rule.Type == "" || rule.Hook == "" {
		writeErr(w, http.StatusBadRequest, "name, type, and hook are required")
		return
	}
	if rule.Config == "" {
		rule.Config = "{}"
	}
	if err := h.store.CreateGuardrailRule(&rule); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.guardrailEngine != nil {
		_ = h.guardrailEngine.Reload()
		h.fireReload()
	}
	writeJSON(w, http.StatusCreated, rule)
}

// UpdateGuardrailRule updates an existing guardrail rule and reloads the engine.
func (h *Handler) UpdateGuardrailRule(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	existing, err := h.store.GetGuardrailRule(id)
	if err != nil || existing == nil {
		writeErr(w, http.StatusNotFound, "rule not found")
		return
	}
	var rule model.GuardrailRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	rule.ID = id
	rule.CreatedAt = existing.CreatedAt
	rule.UpdatedAt = time.Now()
	if err := h.store.UpdateGuardrailRule(&rule); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.guardrailEngine != nil {
		_ = h.guardrailEngine.Reload()
		h.fireReload()
	}
	writeJSON(w, http.StatusOK, rule)
}

// DeleteGuardrailRule deletes a guardrail rule and reloads the engine.
func (h *Handler) DeleteGuardrailRule(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := h.store.DeleteGuardrailRule(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.guardrailEngine != nil {
		_ = h.guardrailEngine.Reload()
		h.fireReload()
	}
	w.WriteHeader(http.StatusOK)
}

// ListGuardrailEvents returns guardrail violation events, optionally
// filtered by token_id.
func (h *Handler) ListGuardrailEvents(w http.ResponseWriter, r *http.Request) {
	tokenID, _ := strconv.ParseInt(r.URL.Query().Get("token_id"), 10, 64)
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	events, err := h.store.GetGuardrailEvents(tokenID, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []model.GuardrailEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}