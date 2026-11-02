package clientapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newControlIngestor(t *testing.T) *Ingestor {
	t.Helper()
	return NewIngestor(nil, []EdgeClient{{Name: "edge-1", Token: "tok-1"}})
}

func doControl(t *testing.T, ing *Ingestor, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	ing.Handler().ServeHTTP(rec, r)
	return rec
}

// ---------- heartbeat ----------

func TestHeartbeat_ReportsStaleConfig(t *testing.T) {
	ing := newControlIngestor(t)

	// A client on version 0 needs the initial config.
	rec := doControl(t, ing, http.MethodPost, "/client/v1/heartbeat", "tok-1",
		`{"client_version":"v0.1.0","config_version":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp heartbeatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ServerVersion != 1 {
		t.Fatalf("server_version = %d, want 1", resp.ServerVersion)
	}
	if !resp.NeedConfig {
		t.Fatal("config_version 0 must report need_config")
	}
	if resp.ServerTime == "" {
		t.Fatal("server_time missing")
	}
}

// A client already on the current version must not refetch.
func TestHeartbeat_UpToDate(t *testing.T) {
	ing := newControlIngestor(t)

	rec := doControl(t, ing, http.MethodPost, "/client/v1/heartbeat", "tok-1",
		`{"client_version":"v0.1.0","config_version":1}`)
	var resp heartbeatResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.NeedConfig {
		t.Fatal("client on the current version must not be told to refetch")
	}
}

// After a push, clients on the old version are told to refetch.
func TestHeartbeat_TellsClientsToRefetchAfterPush(t *testing.T) {
	ing := newControlIngestor(t)
	v := ing.PushBundle(normalizeBundle(ConfigBundle{
		Cache: BundleCache{Enabled: false},
	}))
	if v != 2 {
		t.Fatalf("pushed version = %d, want 2", v)
	}

	rec := doControl(t, ing, http.MethodPost, "/client/v1/heartbeat", "tok-1",
		`{"config_version":1}`)
	var resp heartbeatResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.NeedConfig || resp.ServerVersion != 2 {
		t.Fatalf("resp = %+v, want need_config on version 2", resp)
	}
}

func TestHeartbeat_RequiresAuth(t *testing.T) {
	ing := newControlIngestor(t)
	rec := doControl(t, ing, http.MethodPost, "/client/v1/heartbeat", "wrong", `{}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHeartbeat_RejectsGET(t *testing.T) {
	ing := newControlIngestor(t)
	rec := doControl(t, ing, http.MethodGet, "/client/v1/heartbeat", "tok-1", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// ---------- config bundle ----------

func TestConfig_SignatureVerifies(t *testing.T) {
	ing := newControlIngestor(t)

	rec := doControl(t, ing, http.MethodGet, "/client/v1/config", "tok-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var sb signedBundle
	if err := json.Unmarshal(rec.Body.Bytes(), &sb); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sb.Algorithm != "hmac-sha256" {
		t.Fatalf("algorithm = %q", sb.Algorithm)
	}
	if sb.Signature == "" {
		t.Fatal("signature missing")
	}
	if !VerifyBundle(sb.Config, sb.Signature, "tok-1") {
		t.Fatal("signature must verify with the client's own token")
	}
}

// A bundle signed for one client must not verify for another: that is
// what prevents replaying a captured config at a different edge.
func TestConfig_SignatureIsPerClient(t *testing.T) {
	ing := NewIngestor(nil, []EdgeClient{
		{Name: "a", Token: "tok-a"},
		{Name: "b", Token: "tok-b"},
	})

	rec := doControl(t, ing, http.MethodGet, "/client/v1/config", "tok-a", "")
	var sb signedBundle
	_ = json.Unmarshal(rec.Body.Bytes(), &sb)

	if VerifyBundle(sb.Config, sb.Signature, "tok-b") {
		t.Fatal("a bundle signed for client a must not verify for client b")
	}
}

func TestVerifyBundle_RejectsTamperedConfig(t *testing.T) {
	ing := newControlIngestor(t)
	rec := doControl(t, ing, http.MethodGet, "/client/v1/config", "tok-1", "")
	var sb signedBundle
	_ = json.Unmarshal(rec.Body.Bytes(), &sb)

	// Flip a routing knob — a MITM trying to point the edge elsewhere.
	sb.Config.Fallback.Mode = "local"
	if VerifyBundle(sb.Config, sb.Signature, "tok-1") {
		t.Fatal("tampered bundle must fail verification")
	}
}

func TestConfig_ReflectsPushedBundle(t *testing.T) {
	ing := newControlIngestor(t)
	ing.PushBundle(normalizeBundle(ConfigBundle{
		Servers:  []string{"https://a.example", "https://b.example"},
		Cache:    BundleCache{Enabled: true, TTLSec: 120, MaxItems: 50},
		Fallback: BundleFallback{Mode: "local"},
		Limits:   BundleLimits{MaxInflight: 16},
	}))

	rec := doControl(t, ing, http.MethodGet, "/client/v1/config", "tok-1", "")
	var sb signedBundle
	_ = json.Unmarshal(rec.Body.Bytes(), &sb)

	if len(sb.Config.Servers) != 2 || sb.Config.Servers[1] != "https://b.example" {
		t.Fatalf("servers = %v", sb.Config.Servers)
	}
	if sb.Config.Cache.TTLSec != 120 || sb.Config.Cache.MaxItems != 50 {
		t.Fatalf("cache = %+v", sb.Config.Cache)
	}
	if sb.Config.Fallback.Mode != "local" {
		t.Fatalf("fallback = %+v", sb.Config.Fallback)
	}
	if sb.Config.Limits.MaxInflight != 16 {
		t.Fatalf("limits = %+v", sb.Config.Limits)
	}
	if !VerifyBundle(sb.Config, sb.Signature, "tok-1") {
		t.Fatal("pushed bundle must still verify")
	}
}

func TestConfig_RejectsPOST(t *testing.T) {
	ing := newControlIngestor(t)
	rec := doControl(t, ing, http.MethodPost, "/client/v1/config", "tok-1", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// ---------- defaults / normalization ----------

func TestNormalizeBundle_Defaults(t *testing.T) {
	b := normalizeBundle(ConfigBundle{})
	if b.Cache.TTLSec != 3600 || b.Cache.MaxItems != 2000 {
		t.Fatalf("cache defaults = %+v", b.Cache)
	}
	if b.Fallback.Mode != "cache_then_fail" {
		t.Fatalf("fallback default = %q", b.Fallback.Mode)
	}
	if b.Limits.MaxInflight != 64 {
		t.Fatalf("limits default = %+v", b.Limits)
	}
}

func TestNormalizeBundle_RejectsUnknownFallbackMode(t *testing.T) {
	b := normalizeBundle(ConfigBundle{Fallback: BundleFallback{Mode: "yolo"}})
	if b.Fallback.Mode != "cache_then_fail" {
		t.Fatalf("unknown mode must fall back to cache_then_fail, got %q", b.Fallback.Mode)
	}
}

// ---------- status ----------

func TestStatuses_TracksLastSeen(t *testing.T) {
	ing := newControlIngestor(t)
	_ = doControl(t, ing, http.MethodPost, "/client/v1/heartbeat", "tok-1",
		`{"client_version":"v9.9.9","config_version":1}`)

	list := ing.mergedStatuses()
	if len(list) != 1 {
		t.Fatalf("clients = %d", len(list))
	}
	c := list[0]
	if c.Name != "edge-1" {
		t.Fatalf("name = %q", c.Name)
	}
	if c.ClientVersion != "v9.9.9" {
		t.Fatalf("version = %q", c.ClientVersion)
	}
	if c.LastSeen == nil || !c.Online {
		t.Fatalf("client should be online right after a heartbeat: %+v", c)
	}
}

func TestStatuses_OfflineAfterWindow(t *testing.T) {
	ing := newControlIngestor(t)
	// Record a heartbeat far in the past directly.
	ing.control.mu.Lock()
	ing.control.lastSeen["edge-1"] = time.Now().Add(-10 * time.Minute)
	ing.control.mu.Unlock()

	list := ing.mergedStatuses()
	if len(list) != 1 || list[0].Online {
		t.Fatalf("stale client must be offline: %+v", list)
	}
}

func TestAdminStatus_IncludesConfigVersion(t *testing.T) {
	ing := newControlIngestor(t)
	ing.PushBundle(normalizeBundle(ConfigBundle{}))

	rec := httptest.NewRecorder()
	ing.AdminStatusHandler()(rec, httptest.NewRequest(http.MethodGet, "/admin/edge-clients", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Clients       []clientStatus `json:"clients"`
		ConfigVersion int            `json:"config_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ConfigVersion != 2 {
		t.Fatalf("config_version = %d, want 2", resp.ConfigVersion)
	}
}

// The control plane must not exist when no clients are configured.
func TestControlDisabledWithoutClients(t *testing.T) {
	ing := NewIngestor(nil, nil)
	if ing.controlEnabled() {
		t.Fatal("control plane must be disabled with no clients")
	}
	rec := doControl(t, ing, http.MethodGet, "/client/v1/config", "tok", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
