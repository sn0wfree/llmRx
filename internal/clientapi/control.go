package clientapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/logging"
)

// Control plane for edge clients (P15 phase 2).
//
// The client pulls; the server never dials out. Two operations:
//
//	POST /client/v1/heartbeat  — 30s liveness + config-version exchange
//	GET  /client/v1/config     — signed config bundle
//
// The bundle is HMAC-signed with the client's own token, so a
// man-in-the-middle cannot inject a malicious config: only the holder
// of the shared secret can produce a valid signature. Clients verify
// before applying and fall back to their last good config on failure.

// ConfigBundle is the payload pushed to edge clients. Deliberately small
// and free of upstream secrets: the client needs routing knobs and
// cache settings, never provider keys.
type ConfigBundle struct {
	Version   int       `json:"version"`
	IssuedAt  time.Time `json:"issued_at"`
	Servers   []string  `json:"servers,omitempty"`
	Cache     BundleCache `json:"cache"`
	Fallback  BundleFallback `json:"fallback"`
	Limits    BundleLimits `json:"limits"`
	MinClientVersion string `json:"min_client_version,omitempty"`
}

type BundleCache struct {
	Enabled  bool `json:"enabled"`
	TTLSec   int  `json:"ttl_sec"`
	MaxItems int  `json:"max_items"`
}

type BundleFallback struct {
	// Mode is "cache_then_fail" (default) or "local".
	Mode string `json:"mode"`
}

type BundleLimits struct {
	MaxInflight int `json:"max_inflight"`
}

// controlState holds the mutable control-plane state: the current
// bundle version, the bundle body, and per-client last-seen tracking.
type controlState struct {
	mu       sync.RWMutex
	clients  []EdgeClient
	version  int
	bundle   ConfigBundle
	lastSeen map[string]time.Time
	lastVer  map[string]string
}

func newControlState(clients []EdgeClient) *controlState {
	return &controlState{
		clients:  clients,
		version:  1,
		bundle:   defaultBundle(),
		lastSeen: map[string]time.Time{},
		lastVer:  map[string]string{},
	}
}

// defaultBundle is what a client gets before an operator pushes
// anything: cache on for an hour, fall back to cache then fail.
func defaultBundle() ConfigBundle {
	return ConfigBundle{
		Version:  1,
		IssuedAt: time.Now().UTC(),
		Cache:    BundleCache{Enabled: true, TTLSec: 3600, MaxItems: 2000},
		Fallback: BundleFallback{Mode: "cache_then_fail"},
		Limits:   BundleLimits{MaxInflight: 64},
	}
}

// BumpVersion replaces the bundle contents and increments the version.
// Called from the admin API when an operator edits edge settings.
func (s *controlState) bump(b ConfigBundle) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version++
	b.Version = s.version
	b.IssuedAt = time.Now().UTC()
	s.bundle = b
	return s.version
}

func (s *controlState) snapshot() ConfigBundle {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bundle
}

func (s *controlState) touch(client, clientVersion string, now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSeen[client] = now
	if clientVersion != "" {
		s.lastVer[client] = clientVersion
	}
	return s.version
}

// status reports clients that have checked in recently.
type clientStatus struct {
	Name          string     `json:"name"`
	LastSeen      *time.Time `json:"last_seen,omitempty"`
	ClientVersion string     `json:"client_version,omitempty"`
	Online        bool       `json:"online"`
}

func (s *controlState) statuses(now time.Time, onlineWithin time.Duration) []clientStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]clientStatus, 0, len(s.clients))
	for _, c := range s.clients {
		st := clientStatus{Name: c.Name, ClientVersion: s.lastVer[c.Name]}
		if t, ok := s.lastSeen[c.Name]; ok {
			t := t
			st.LastSeen = &t
			st.Online = now.Sub(t) <= onlineWithin
		}
		out = append(out, st)
	}
	return out
}

// heartbeatRequest is what the client sends every 30s.
type heartbeatRequest struct {
	ClientVersion string `json:"client_version"`
	ConfigVersion int    `json:"config_version"`
	PendingUsage  int    `json:"pending_usage"`
	CacheHits     int64  `json:"cache_hits"`
	CacheMisses   int64  `json:"cache_misses"`
}

// heartbeatResponse tells the client whether to fetch a new config.
type heartbeatResponse struct {
	ServerVersion int    `json:"server_version"`
	NeedConfig    bool   `json:"need_config"`
	ServerTime    string `json:"server_time"`
}

// handleHeartbeat records liveness and reports whether the client's
// config is stale.
func (s *controlState) handleHeartbeat(w http.ResponseWriter, r *http.Request, clientName string) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "heartbeat requires POST")
		return
	}
	var req heartbeatRequest
	// A malformed body is not fatal: liveness alone is still useful,
	// so decode best-effort.
	_ = json.NewDecoder(r.Body).Decode(&req)

	now := time.Now().UTC()
	serverVersion := s.touch(clientName, req.ClientVersion, now)

	writeJSON(w, http.StatusOK, heartbeatResponse{
		ServerVersion: serverVersion,
		NeedConfig:    req.ConfigVersion != serverVersion,
		ServerTime:    now.Format(time.RFC3339),
	})
}

// signedBundle is the wire form: the bundle plus its signature.
type signedBundle struct {
	Config    ConfigBundle `json:"config"`
	Signature string       `json:"signature"`
	Algorithm string       `json:"algorithm"`
}

// handleConfig returns the current bundle, signed with the requesting
// client's token. The signature binds the bundle to that client, so a
// bundle captured from one edge cannot be replayed at another.
func (s *controlState) handleConfig(w http.ResponseWriter, r *http.Request, clientName, clientToken string) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "config requires GET")
		return
	}
	b := s.snapshot()
	sig, err := signBundle(b, clientToken)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "sign config: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Config can be cached briefly; the version check drives refreshes.
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(signedBundle{
		Config:    b,
		Signature: sig,
		Algorithm: "hmac-sha256",
	})
}

// signBundle produces the hex HMAC over the canonical JSON encoding of
// the bundle. Canonical = the struct's own field order, which
// encoding/json guarantees for structs.
func signBundle(b ConfigBundle, key string) (string, error) {
	// Sign a stable projection: IssuedAt is included so two bundles with
	// the same version but different timestamps cannot be confused.
	payload, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// VerifyBundle is the client-side counterpart. Exported so the client
// package (and tests) can validate a bundle before applying it.
func VerifyBundle(b ConfigBundle, signature, key string) bool {
	want, err := signBundle(b, key)
	if err != nil {
		return false
	}
	// Constant-time compare: a timing oracle here would let an attacker
	// forge a signature byte by byte.
	return hmac.Equal([]byte(want), []byte(signature))
}

// ---------- admin surface ----------

// EdgeClientsStatus is the read model for the admin page.
func (ing *Ingestor) EdgeClientsStatus() []clientStatus {
	if ing.control == nil {
		return nil
	}
	return ing.control.statuses(time.Now().UTC(), 90*time.Second)
}

// handleAdminStatus serves the admin-visible client list. Mounted
// outside /client/v1 (see server.go) so it sits behind the admin
// session guard.
func (ing *Ingestor) AdminStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"clients": ing.EdgeClientsStatus(),
			"config_version": func() int {
				if ing.control == nil {
					return 0
				}
				return ing.control.snapshot().Version
			}(),
		})
	}
}

// PushBundle replaces the config bundle for all clients. Called from
// the admin API; returns the new version.
func (ing *Ingestor) PushBundle(b ConfigBundle) int {
	if ing.control == nil {
		return 0
	}
	v := ing.control.bump(b)
	logging.Info("edge config bundle pushed", logging.F("version", v))
	return v
}

// CurrentBundle returns the active bundle (admin read-back).
func (ing *Ingestor) CurrentBundle() ConfigBundle {
	if ing.control == nil {
		return ConfigBundle{}
	}
	return ing.control.snapshot()
}

// controlEnabled reports whether the control plane is wired.
func (ing *Ingestor) controlEnabled() bool { return ing.control != nil }

// ---------- admin adapters ----------
//
// These return plain JSON-marshalable values so internal/admin can stay
// free of this package (and therefore of the log store's CGO
// dependency).

// EdgeClientsStatusJSON exposes the client list to the admin API.
func (ing *Ingestor) EdgeClientsStatusJSON() any {
	if ing.control == nil {
		return []clientStatus{}
	}
	return ing.control.statuses(time.Now().UTC(), 90*time.Second)
}

// CurrentBundleJSON exposes the active bundle to the admin API.
func (ing *Ingestor) CurrentBundleJSON() any {
	if ing.control == nil {
		return ConfigBundle{}
	}
	return ing.control.snapshot()
}

// PushBundleJSON decodes and applies an admin-pushed bundle, returning
// the new version.
func (ing *Ingestor) PushBundleJSON(raw json.RawMessage) (int, error) {
	if ing.control == nil {
		return 0, fmt.Errorf("control plane disabled")
	}
	var b ConfigBundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return 0, fmt.Errorf("invalid bundle: %w", err)
	}
	// The version and issue time are server-assigned; a client-supplied
	// value would let an operator (or a stray script) pin a stale
	// version and confuse the pull logic.
	b.Version = 0
	b.IssuedAt = time.Time{}
	return ing.PushBundle(normalizeBundle(b)), nil
}

// normalizeBundle fills defaults so a partially-specified push still
// produces a usable config.
func normalizeBundle(b ConfigBundle) ConfigBundle {
	if b.Cache.TTLSec <= 0 {
		b.Cache.TTLSec = 3600
	}
	if b.Cache.MaxItems <= 0 {
		b.Cache.MaxItems = 2000
	}
	switch b.Fallback.Mode {
	case "local", "cache_then_fail":
	default:
		b.Fallback.Mode = "cache_then_fail"
	}
	if b.Limits.MaxInflight <= 0 {
		b.Limits.MaxInflight = 64
	}
	return b
}
