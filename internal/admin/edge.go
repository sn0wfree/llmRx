package admin

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// EdgeControl is the narrow control-plane surface the admin API needs.
// Declared here so internal/admin does not import internal/clientapi
// (which depends on the log store, and thus CGO).
type EdgeControl interface {
	EdgeClientsStatusJSON() any
	CurrentBundleJSON() any
	PushBundleJSON(raw json.RawMessage) (int, error)
	// Enroll support (P15 phase 3).
	IssueEnrollTokenJSON(clientName string) (any, error)
	RevokeEdgeClientJSON(clientID string) error
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

// IssueEnrollToken creates a single-use enrollment code. The response
// carries the plaintext token exactly once — it is not recoverable.
func (h *Handler) IssueEnrollToken(w http.ResponseWriter, r *http.Request) {
	if h.edgeIngestor == nil {
		writeErr(w, http.StatusServiceUnavailable, "no edge clients configured")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	out, err := h.edgeIngestor.IssueEnrollTokenJSON(req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// RevokeEdgeClient disables an enrolled client. Idempotent.
func (h *Handler) RevokeEdgeClient(w http.ResponseWriter, r *http.Request) {
	if h.edgeIngestor == nil {
		writeErr(w, http.StatusServiceUnavailable, "no edge clients configured")
		return
	}
	clientID := chi.URLParam(r, "id")
	if clientID == "" {
		writeErr(w, http.StatusBadRequest, "client id required")
		return
	}
	if err := h.edgeIngestor.RevokeEdgeClientJSON(clientID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": clientID})
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
