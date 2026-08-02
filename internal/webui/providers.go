package webui

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/modelmeta"
	"github.com/sn0wfree/llmRx/internal/provider"
)

// ChannelFetchModels calls the upstream API to discover available
// models for a channel. Returns a JSON list of model IDs. The UI
// uses this to show a preview/select dialog before applying.
func (h *Handler) ChannelFetchModels(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ch, err := h.store.GetChannel(parseInt64Default(id, 0))
	if err != nil || ch == nil {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}
	keys, err := h.store.GetKeys(ch.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(keys) == 0 || keys[0].Key == "" {
		http.Error(w, "channel has no API key", http.StatusBadRequest)
		return
	}
	models, err := provider.ListModels(r.Context(), ch.Provider, keys[0].Key, ch.BaseURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// ProvidersPage renders the provider management page.
func (h *Handler) ProvidersPage(w http.ResponseWriter, r *http.Request) {
	descs := provider.AllProviders()
	data := map[string]any{
		"Body":      "providers_list_body",
		"Title":     "供应商管理",
		"User":      userToView(getUser(r)),
		"Active":    "providers",
		"Providers": descs,
	}
	if err := h.renderer.Render(w, "providers_list_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ProviderCreate handles the form submission to add a custom provider.
func (h *Handler) ProviderCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	displayName := r.FormValue("display_name")
	protocol := r.FormValue("protocol")
	baseURL := r.FormValue("base_url")
	if name == "" || protocol == "" || baseURL == "" {
		http.Error(w, "name, protocol, and base_url are required", http.StatusBadRequest)
		return
	}
	pd := &model.ProviderDef{
		Name:        name,
		DisplayName: displayName,
		Protocol:    protocol,
		BaseURL:     baseURL,
	}
	if err := h.store.CreateProviderDef(pd); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	provider.RegisterProvider(provider.ProviderDesc{
		Name:           pd.Name,
		DisplayName:    pd.DisplayName,
		Protocol:       pd.Protocol,
		DefaultBaseURL: pd.BaseURL,
		Source:         "db",
	})
	http.Redirect(w, r, "/admin/providers", http.StatusSeeOther)
}

// ProviderDelete removes a custom provider from the DB.
func (h *Handler) ProviderDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	pid, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := h.store.DeleteProviderDef(pid); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ModelsByProvider returns known models for the given provider from
// the model metadata registry. Used by the channel form to let users
// pick models from a catalog instead of typing them manually.
func (h *Handler) ModelsByProvider(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	var models []*modelmeta.ModelMeta
	if provider != "" {
		models = modelmeta.GetByProvider(provider)
	}
	if models == nil {
		models = []*modelmeta.ModelMeta{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// MetaProviders returns all provider names from the model metadata
// registry. Used by token/combo forms to populate the model catalog
// provider selector.
func (h *Handler) MetaProviders(w http.ResponseWriter, r *http.Request) {
	providers := modelmeta.AllProviders()
	if providers == nil {
		providers = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": providers})
}

// AvailableModels returns all model names from configured channels
// (enabled only), deduplicated and sorted. Used by combo forms so
// users can pick from models that actually exist in their setup.
func (h *Handler) AvailableModels(w http.ResponseWriter, r *http.Request) {
	chs, err := h.store.GetChannels()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	seen := make(map[string]bool)
	var models []string
	for i := range chs {
		if chs[i].Status != model.ChannelEnabled {
			continue
		}
		for _, m := range chs[i].Models {
			if !seen[m] {
				seen[m] = true
				models = append(models, m)
			}
		}
	}
	sort.Strings(models)
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// PreviewModels fetches the model list from an upstream provider
// without requiring a saved channel. Used by the new-channel form
// so operators can browse what models are available before creating
// the channel.
func (h *Handler) PreviewModels(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	prov := r.FormValue("provider")
	baseURL := r.FormValue("base_url")
	apiKey := r.FormValue("api_key")
	if prov == "" || baseURL == "" {
		http.Error(w, "provider and base_url are required", http.StatusBadRequest)
		return
	}
	models, err := provider.ListModels(r.Context(), prov, apiKey, baseURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}
