package admin

import (
	"encoding/json"
	"net/http"
)

// EdgeControl is the narrow control-plane surface the admin API needs.
// Declared here so internal/admin does not import internal/clientapi
// (which depends on the log store, and thus CGO).
type EdgeControl interface {
	EdgeClientsStatusJSON() any
	CurrentBundleJSON() any
	PushBundleJSON(raw json.RawMessage) (int, error)
}

// EdgeClients reports configured edge clients with their last-seen
// state and the active config version.
func (h *Handler) EdgeClients(w http.ResponseWriter, r *http.Request) {
	if h.edgeIngestor == nil {
		writeErr(w, http.StatusServiceUnavailable, "no edge clients configured")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"clients":        h.edgeIngestor.EdgeClientsStatusJSON(),
		"config":         h.edgeIngestor.CurrentBundleJSON(),
	})
}

// PushEdgeConfig replaces the config bundle every edge client will pull
// on its next heartbeat, and returns the new version.
//
// The body is the ConfigBundle JSON. It is passed through as raw JSON so
// this package stays independent of the bundle type's definition.
func (h *Handler) PushEdgeConfig(w http.ResponseWriter, r *http.Request) {
	if h.edgeIngestor == nil {
		writeErr(w, http.StatusServiceUnavailable, "no edge clients configured")
		return
	}
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	version, err := h.edgeIngestor.PushBundleJSON(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config_version": version})
}
