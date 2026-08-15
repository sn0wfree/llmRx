package admin

import (
	"encoding/json"
	"net/http"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/modelmeta"
)

// ListModelCosts returns the full rate card.
func (h *Handler) ListModelCosts(w http.ResponseWriter, r *http.Request) {
	costs, err := h.store.ListModelCosts()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"model_costs": costs})
}

// UpsertModelCost creates or replaces one model's rate card.
func (h *Handler) UpsertModelCost(w http.ResponseWriter, r *http.Request) {
	var c model.ModelCost
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if c.Model == "" {
		writeErr(w, http.StatusBadRequest, "model is required")
		return
	}
	if err := h.store.UpsertModelCost(&c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.reloadModelCosts()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model": c.Model})
}

// DeleteModelCost removes a model from the rate card (requests for
// it become fail-closed 422 until it is priced again).
func (h *Handler) DeleteModelCost(w http.ResponseWriter, r *http.Request) {
	modelName := r.URL.Query().Get("model")
	if modelName == "" {
		writeErr(w, http.StatusBadRequest, "model is required")
		return
	}
	if err := h.store.DeleteModelCost(modelName); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.reloadModelCosts()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ImportModelCosts adds models from the modelmeta catalog that are
// missing from the rate card (existing rows are never overwritten so
// operator-set prices survive). Returns the number added.
func (h *Handler) ImportModelCosts(w http.ResponseWriter, r *http.Request) {
	existing := make(map[string]bool)
	rows, err := h.store.ListModelCosts()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, c := range rows {
		existing[c.Model] = true
	}
	var add []model.ModelCost
	for _, m := range modelmeta.All() {
		if existing[m.ID] {
			continue
		}
		cacheRead := m.CacheReadPrice
		if cacheRead <= 0 {
			cacheRead = m.InputPrice * m.CacheReadDiscount
		}
		add = append(add, model.ModelCost{
			Model:                     m.ID,
			Provider:                  m.Provider,
			InputPricePer1M:           m.InputPrice,
			OutputPricePer1M:          m.OutputPrice,
			CacheReadPricePer1M:       cacheRead,
			CacheWritePricePer1M:      m.CacheWritePrice,
			ReasoningPricePer1M:       m.ReasoningPrice,
			ImagePrice:                m.ImagePrice,
			InputPricePer1MAbove128K:  m.InputPriceAbove128K,
			OutputPricePer1MAbove128K: m.OutputPriceAbove128K,
			InputPricePer1MAbove200K:  m.InputPriceAbove200K,
			OutputPricePer1MAbove200K: m.OutputPriceAbove200K,
			ContextWindow:             m.ContextWindow,
			MaxOutput:                 m.MaxOutput,
		})
	}
	if len(add) > 0 {
		if err := h.store.BatchUpsertModelCosts(add); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	h.reloadModelCosts()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": len(add)})
}

func (h *Handler) reloadModelCosts() {
	if h.modelCosts != nil {
		_ = h.modelCosts.Reload()
	}
}
