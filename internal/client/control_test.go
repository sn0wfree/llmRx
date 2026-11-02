package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// verifyBundle on the client must agree with the server's signBundle.
// A mismatch here is the single worst failure mode of this feature:
// every config push would be rejected in production while the unit
// tests still passed on either side alone.

// signForTest reproduces the server's canonical signature so the client
// verifier can be exercised without importing internal/clientapi.
func signForTest(t *testing.T, b Bundle, token string) string {
	t.Helper()
	payload, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return hmacHex(payload, token)
}

// fakeControlServer serves the two control endpoints with a fixed
// bundle and records how many times each was hit.
type fakeControlServer struct {
	*httptest.Server
	heartbeats atomic.Int64
	configs    atomic.Int64
}

func newFakeControl(t *testing.T, token string, b Bundle, needConfig bool) *fakeControlServer {
	t.Helper()
	f := &fakeControlServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/client/v1/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		f.heartbeats.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"server_version":` + itoa64(int64(b.Version)) +
			`,"need_config":` + boolStr(needConfig) + `}`))
	})
	mux.HandleFunc("/client/v1/config", func(w http.ResponseWriter, r *http.Request) {
		f.configs.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"config":    b,
			"signature": signForTest(t, b, token),
			"algorithm": "hmac-sha256",
		})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func testBundle() Bundle {
	var b Bundle
	b.Version = 3
	b.IssuedAt = time.Unix(1761900000, 0).UTC()
	b.Servers = []string{"https://new-a.example", "https://new-b.example"}
	b.Cache.Enabled = true
	b.Cache.TTLSec = 120
	b.Cache.MaxItems = 50
	b.Fallback.Mode = "local"
	b.Limits.MaxInflight = 16
	return b
}

// ---------- signature ----------

func TestVerifyBundle_AcceptsValid(t *testing.T) {
	b := testBundle()
	sig := signForTest(t, b, "tok")
	if !verifyBundle(b, sig, "tok") {
		t.Fatal("valid signature rejected")
	}
}

func TestVerifyBundle_RejectsWrongToken(t *testing.T) {
	b := testBundle()
	sig := signForTest(t, b, "tok")
	if verifyBundle(b, sig, "other") {
		t.Fatal("signature must not verify under a different token")
	}
}

// Any change to the bundle body must invalidate the signature — this is
// what stops a MITM from redirecting the client to their own server.
func TestVerifyBundle_RejectsTampered(t *testing.T) {
	b := testBundle()
	sig := signForTest(t, b, "tok")

	b.Servers = []string{"https://attacker.example"}
	if verifyBundle(b, sig, "tok") {
		t.Fatal("tampered bundle must fail verification")
	}
}

// ---------- polling ----------

func TestControl_FetchesAndAppliesBundle(t *testing.T) {
	b := testBundle()
	srv := newFakeControl(t, "tok", b, true)

	ctl := NewControl(srv.URL, "edge-1", "tok", "v1")
	var applied Bundle
	var got atomic.Int64
	ctl.OnBundle(func(in Bundle) {
		applied = in
		got.Add(1)
	})

	ctl.round(context.Background())

	if srv.heartbeats.Load() != 1 {
		t.Fatalf("heartbeats = %d", srv.heartbeats.Load())
	}
	if srv.configs.Load() != 1 {
		t.Fatalf("config fetches = %d", srv.configs.Load())
	}
	if got.Load() != 1 {
		t.Fatal("OnBundle not invoked")
	}
	if applied.Version != 3 || len(applied.Servers) != 2 {
		t.Fatalf("applied = %+v", applied)
	}
	if ctl.AppliedVersion() != 3 {
		t.Fatalf("AppliedVersion = %d", ctl.AppliedVersion())
	}
	if lastErr, _ := ctl.LastStatus(); lastErr != "" {
		t.Fatalf("unexpected error: %s", lastErr)
	}
}

// No need_config → no fetch. A 30s poll must not pull the bundle every
// time.
func TestControl_SkipsFetchWhenUpToDate(t *testing.T) {
	srv := newFakeControl(t, "tok", testBundle(), false)
	ctl := NewControl(srv.URL, "edge-1", "tok", "v1")

	ctl.round(context.Background())

	if srv.configs.Load() != 0 {
		t.Fatalf("config fetched %d times, want 0", srv.configs.Load())
	}
}

// A bundle failing verification must be discarded: the previous config
// stays in force and the error is surfaced.
func TestControl_RejectsBadSignature(t *testing.T) {
	// Server signs with a different key than the client holds.
	b := testBundle()
	mux := http.NewServeMux()
	mux.HandleFunc("/client/v1/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"server_version":3,"need_config":true}`))
	})
	mux.HandleFunc("/client/v1/config", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"config":    b,
			"signature": signForTest(t, b, "server-key"),
			"algorithm": "hmac-sha256",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctl := NewControl(srv.URL, "edge-1", "client-key", "v1")
	var called atomic.Int64
	ctl.OnBundle(func(Bundle) { called.Add(1) })

	ctl.round(context.Background())

	if called.Load() != 0 {
		t.Fatal("an unverified bundle must never reach OnBundle")
	}
	if ctl.AppliedVersion() != 0 {
		t.Fatalf("AppliedVersion = %d, want 0", ctl.AppliedVersion())
	}
	lastErr, _ := ctl.LastStatus()
	if !strings.Contains(lastErr, "signature") {
		t.Fatalf("last error = %q, want a signature failure", lastErr)
	}
}

// A stale/replayed bundle must not roll a client backwards.
func TestControl_RefusesDowngrade(t *testing.T) {
	b := testBundle()
	b.Version = 1
	srv := newFakeControl(t, "tok", b, true)

	ctl := NewControl(srv.URL, "edge-1", "tok", "v1")
	ctl.applied.Store(5) // client already on a newer bundle

	var called atomic.Int64
	ctl.OnBundle(func(Bundle) { called.Add(1) })
	ctl.round(context.Background())

	if called.Load() != 0 {
		t.Fatal("a downgrade must not be applied")
	}
	if ctl.AppliedVersion() != 5 {
		t.Fatalf("AppliedVersion = %d, want 5 (unchanged)", ctl.AppliedVersion())
	}
	lastErr, _ := ctl.LastStatus()
	if !strings.Contains(lastErr, "downgrade") {
		t.Fatalf("last error = %q, want a downgrade refusal", lastErr)
	}
}

func TestControl_RejectsUnknownAlgorithm(t *testing.T) {
	b := testBundle()
	mux := http.NewServeMux()
	mux.HandleFunc("/client/v1/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"server_version":3,"need_config":true}`))
	})
	mux.HandleFunc("/client/v1/config", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"config":    b,
			"signature": "x",
			"algorithm": "md5",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctl := NewControl(srv.URL, "edge-1", "tok", "v1")
	ctl.round(context.Background())
	lastErr, _ := ctl.LastStatus()
	if !strings.Contains(lastErr, "algorithm") {
		t.Fatalf("last error = %q, want an algorithm rejection", lastErr)
	}
}

func TestControl_HeartbeatFailureRecorded(t *testing.T) {
	ctl := NewControl("http://127.0.0.1:1", "edge-1", "tok", "v1")
	ctl.round(context.Background())
	lastErr, lastOK := ctl.LastStatus()
	if lastErr == "" {
		t.Fatal("a connection failure must be recorded")
	}
	if !lastOK.IsZero() {
		t.Fatal("lastOK must stay zero on failure")
	}
}

// ---------- ApplyBundle ----------

func TestApplyBundle_UpdatesLiveConfig(t *testing.T) {
	cfg := &Config{
		Listen:  "127.0.0.1:0",
		Servers: []string{"https://old.example"},
		Token:   "sk-local",
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	b := testBundle()
	cfg.ApplyBundle(b)

	got := cfg.ServersSnapshot()
	if len(got) != 2 || got[0] != "https://new-a.example" {
		t.Fatalf("servers = %v", got)
	}
	if !cfg.CacheEnabled || cfg.CacheTTLSec != 120 || cfg.CacheMaxItems != 50 {
		t.Fatalf("cache not applied: %+v", cfg)
	}
	if cfg.FallbackModeOrDefault() != "local" {
		t.Fatalf("fallback mode = %q", cfg.FallbackModeOrDefault())
	}
	if cfg.MaxInflight != 16 {
		t.Fatalf("max_inflight = %d", cfg.MaxInflight)
	}
	// Credentials are local-only and must never be overwritten by a
	// network-pushed bundle.
	if cfg.Token != "sk-local" {
		t.Fatalf("token must not be changed by a bundle, got %q", cfg.Token)
	}
}

// An empty bundle must not clobber the local config.
func TestApplyBundle_EmptyIsNoOp(t *testing.T) {
	cfg := &Config{Servers: []string{"https://keep.example"}}
	_ = cfg.Normalize()
	before := cfg.ServersSnapshot()

	cfg.ApplyBundle(Bundle{})

	after := cfg.ServersSnapshot()
	if len(after) != len(before) || after[0] != before[0] {
		t.Fatalf("servers changed by an empty bundle: %v", after)
	}
}

// ---------- /healthz surface ----------

func TestHealthz_ReportsControlState(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()

	cfg := &Config{Listen: "127.0.0.1:0", Servers: []string{up.URL}}
	_ = cfg.Normalize()
	p := NewProxy(cfg)
	ctl := NewControl(up.URL, "edge-1", "tok", "v1")
	p.SetControl(ctl)

	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["config_version"]; !ok {
		t.Fatalf("config_version missing: %v", out)
	}
	if _, ok := out["control_last_error"]; !ok {
		t.Fatalf("control_last_error missing: %v", out)
	}
}
