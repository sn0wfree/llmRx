package webui

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/model"
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
	var pid int64
	for _, c := range id {
		if c >= '0' && c <= '9' {
			pid = pid*10 + int64(c-'0')
		}
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
