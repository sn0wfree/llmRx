package webui

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/model"
)

// ModelSetsPage renders the top-level list of all model sets across
// every token. Filters: q (search by name), token_id (filter by
// token), enabled (all/yes/no).
func (h *Handler) ModelSetsPage(w http.ResponseWriter, r *http.Request) {
	h.modelSetsListPage(w, r, "")
}

func (h *Handler) modelSetsListPage(w http.ResponseWriter, r *http.Request, query string) {
	all, err := h.store.ListAllComboModels()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tokenFilter := r.URL.Query().Get("token_id")
	enabledFilter := r.URL.Query().Get("enabled")

	tokens, _ := h.store.GetTokens()
	tokenByID := make(map[int64]string, len(tokens))
	for _, t := range tokens {
		tokenByID[t.ID] = t.Name
	}

	filtered := all[:0]
	for _, c := range all {
		if query != "" && !strings.Contains(strings.ToLower(c.Name), strings.ToLower(query)) {
			continue
		}
		if tokenFilter != "" {
			if id, err := strconv.ParseInt(tokenFilter, 10, 64); err == nil && c.TokenID != id {
				continue
			}
		}
		if enabledFilter == "yes" && !c.Enabled {
			continue
		}
		if enabledFilter == "no" && c.Enabled {
			continue
		}
		filtered = append(filtered, c)
	}

	data := map[string]any{
		"Body":          "model_sets_list_body",
		"Title":         "模型集",
		"User":          userToView(getUser(r)),
		"Active":        "model-sets",
		"Combos":        filtered,
		"Tokens":        tokens,
		"TokenByID":     tokenByID,
		"TokenFilter":   tokenFilter,
		"EnabledFilter": enabledFilter,
		"Query":         query,
	}
	if strings.HasPrefix(r.URL.Path, "/partial/") {
		if err := h.renderer.RenderPartial(w, "model_sets_table_body", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	if err := h.renderer.Render(w, "model_sets_list_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ModelSetsListPartial returns the table body for HTMX search/filter.
func (h *Handler) ModelSetsListPartial(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	h.modelSetsListPage(w, r, q)
}

// ModelSetDetailPage renders a single model set with the model→channel
// visualization (which channels support each underlying model).
func (h *Handler) ModelSetDetailPage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	c, err := h.store.GetComboModel(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	token, _ := h.store.GetTokenByID(c.TokenID)
	channels, _ := h.store.GetChannels()

	type modelCoverage struct {
		Model    string
		Channels []channelRef
	}
	covByName := make(map[string]*modelCoverage, len(c.Models))
	for _, m := range c.Models {
		covByName[m] = &modelCoverage{Model: m}
	}
	for i := range channels {
		ch := &channels[i]
		if ch.Status != model.ChannelEnabled {
			continue
		}
		for _, m := range ch.Models {
			if cov, ok := covByName[m]; ok {
				cov.Channels = append(cov.Channels, channelRef{
					ID:       ch.ID,
					Name:     ch.Name,
					Provider: ch.Provider,
					Priority: ch.Priority,
				})
			}
		}
	}
	coverage := make([]modelCoverage, 0, len(c.Models))
	for _, m := range c.Models {
		if cov, ok := covByName[m]; ok {
			coverage = append(coverage, *cov)
		}
	}

	data := map[string]any{
		"Body":     "model_set_detail_body",
		"Title":    "模型集详情",
		"User":     userToView(getUser(r)),
		"Active":   "model-sets",
		"Combo":    c,
		"Token":    token,
		"Coverage": coverage,
	}
	if err := h.renderer.Render(w, "model_set_detail_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type channelRef struct {
	ID       int64
	Name     string
	Provider string
	Priority int
}
