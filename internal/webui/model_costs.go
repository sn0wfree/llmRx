package webui

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/modelmeta"
)

// ModelCostsPage renders the rate-card editor: all model_costs rows
// with inline-edit forms plus an import-from-modelmeta control.
func (h *Handler) ModelCostsPage(w http.ResponseWriter, r *http.Request) {
	costs, err := h.store.ListModelCosts()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Models in the catalog but missing from the rate card (offer to import).
	existing := make(map[string]bool)
	for _, c := range costs {
		existing[c.Model] = true
	}
	var missing []*modelmeta.ModelMeta
	for _, m := range modelmeta.All() {
		if !existing[m.ID] {
			missing = append(missing, m)
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].ID < missing[j].ID })

	data := map[string]any{
		"Body":    "model_costs_body",
		"Title":   "模型价格",
		"User":    userToView(getUser(r)),
		"Active":  "model-costs",
		"Costs":   costs,
		"Missing": missing,
	}
	if err := h.renderer.Render(w, "model_costs_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ModelCostCreate adds a brand-new model rate card.
func (h *Handler) ModelCostCreate(w http.ResponseWriter, r *http.Request) {
	var c model.ModelCost
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	c.Model = strings.TrimSpace(c.Model)
	if c.Model == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}
	if err := h.store.UpsertModelCost(&c); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.respondOK(w, r)
}

// ModelCostUpdate edits an existing model rate card.
func (h *Handler) ModelCostUpdate(w http.ResponseWriter, r *http.Request) {
	modelName := chi.URLParam(r, "model")
	var c model.ModelCost
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	c.Model = modelName
	if err := h.store.UpsertModelCost(&c); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.respondOK(w, r)
}

// ModelCostDelete removes a model from the rate card.
func (h *Handler) ModelCostDelete(w http.ResponseWriter, r *http.Request) {
	modelName := chi.URLParam(r, "model")
	if err := h.store.DeleteModelCost(modelName); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.respondOK(w, r)
}

// ModelCostImport adds catalog models missing from the rate card.
func (h *Handler) ModelCostImport(w http.ResponseWriter, r *http.Request) {
	existing := make(map[string]bool)
	rows, err := h.store.ListModelCosts()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	h.respondOK(w, r)
}

func (h *Handler) respondOK(w http.ResponseWriter, r *http.Request) {
	// HTMX targets typically reload the table via a partial; a simple
	// JSON ack works for fetch-based forms.
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}
