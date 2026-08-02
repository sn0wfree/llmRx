package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sn0wfree/llmRx/internal/store"
)

type atomicInt64 struct{ v int64 }

func (a *atomicInt64) Add(n int64) int64   { return atomic.AddInt64(&a.v, n) }
func (a *atomicInt64) Load() int64         { return atomic.LoadInt64(&a.v) }

type atomicBool struct{ v int32 }

func (a *atomicBool) Store(b bool) { var n int32; if b { n = 1 }; atomic.StoreInt32(&a.v, n) }
func (a *atomicBool) Load() bool   { return atomic.LoadInt32(&a.v) == 1 }

// fakeOAuthServer simulates an RFC 8414 + 7591 + 8628 authorization
// server for tests.
type fakeOAuthServer struct {
	t               *testing.T
	ts              *httptest.Server
	registrationHits atomicInt64
	deviceHits       atomicInt64
	tokenHits        atomicInt64
	refreshHits      atomicInt64
	// pendingUntil: token endpoint returns authorization_pending
	// until this many poll calls.
	pendingPolls atomicInt64
}

func newFakeOAuthServer(t *testing.T) *fakeOAuthServer {
	f := &fakeOAuthServer{t: t}
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/oauth-authorization-server.mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"issuer":                       f.ts.URL,
			"authorization_endpoint":       f.ts.URL + "/authorize",
			"token_endpoint":               f.ts.URL + "/token",
			"registration_endpoint":        f.ts.URL + "/register",
			"device_authorization_endpoint": f.ts.URL + "/device",
		})
	})

	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		f.registrationHits.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if body["client_name"] != "llmrx" {
			t.Fatalf("unexpected client_name %v", body["client_name"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{
			"client_id":     "registered-client-1",
			"client_secret": "registered-secret-1",
		})
	})

	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		f.deviceHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"device_code":      "dev-code-1",
			"user_code":        "ABCD-1234",
			"verification_uri": f.ts.URL + "/verify",
			"expires_in":       600,
			"interval":         1,
		})
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		grant := r.Form.Get("grant_type")
		switch grant {
		case "urn:ietf:params:oauth:grant-type:device_code":
			f.tokenHits.Add(1)
			if f.pendingPolls.Load() > 0 {
				f.pendingPolls.Add(-1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{
					"error":             "authorization_pending",
					"error_description": "user has not approved yet",
				})
				return
			}
			if r.Form.Get("device_code") != "dev-code-1" {
				t.Fatalf("unexpected device_code %q", r.Form.Get("device_code"))
			}
			if r.Form.Get("client_id") == "" {
				t.Fatal("missing client_id in token request")
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-token-1",
				"refresh_token": "refresh-token-1",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		case "refresh_token":
			f.refreshHits.Add(1)
			if r.Form.Get("refresh_token") != "refresh-token-1" {
				t.Fatalf("unexpected refresh_token %q", r.Form.Get("refresh_token"))
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-token-2",
				"refresh_token": "refresh-token-2",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		default:
			t.Fatalf("unexpected grant_type %q", grant)
		}
	})

	ts := httptest.NewServer(mux)
	f.ts = ts
	return f
}

func (f *fakeOAuthServer) close() { f.ts.Close() }

// mcpRepoForOAuth is a minimal in-memory MCPRepository for OAuth tests.
type mcpRepoForOAuth struct {
	store.MCPRepository
	server *store.MCPServer
}

func (r *mcpRepoForOAuth) GetMCPServer(ctx context.Context, id int64) (*store.MCPServer, error) {
	return r.server, nil
}

func (r *mcpRepoForOAuth) UpdateMCPServer(ctx context.Context, s *store.MCPServer) error {
	r.server = s
	return nil
}

func newOAuthTestServer(t *testing.T) (*fakeOAuthServer, *OAuthManager, *mcpRepoForOAuth, *store.MCPServer) {
	f := newFakeOAuthServer(t)
	repo := &mcpRepoForOAuth{
		server: &store.MCPServer{
			ID:              1,
			Name:            "github",
			URL:             f.ts.URL,
			OAuthConfigJSON: `{"client_id":"","client_secret":"","scopes":["repo"]}`,
			Enabled:         true,
		},
	}
	mgr := NewOAuthManager(repo, nil)
	return f, mgr, repo, repo.server
}

func TestOAuthDiscoverAndDeviceFlow(t *testing.T) {
	f, mgr, _, srv := newOAuthTestServer(t)
	defer f.close()

	st, err := mgr.Authorize(context.Background(), srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "authorizing" {
		t.Fatalf("expected authorizing, got %s", st.Status)
	}
	if st.UserCode != "ABCD-1234" || st.VerificationURI == "" {
		t.Fatalf("unexpected device response: %+v", st)
	}
	if f.registrationHits.Load() == 0 {
		t.Fatal("expected dynamic registration")
	}
	if f.deviceHits.Load() == 0 {
		t.Fatal("expected device request")
	}
	// Registered client_id should be persisted.
	if srv.OAuthConfigJSON == "" || !strings.Contains(srv.OAuthConfigJSON, "registered-client-1") {
		t.Fatalf("expected persisted client_id, got %q", srv.OAuthConfigJSON)
	}

	// Poll once: still pending.
	f.pendingPolls.Add(1)
	poll1, err := mgr.Poll(context.Background(), srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if poll1.Status != "authorizing" {
		t.Fatalf("expected pending, got %s", poll1.Status)
	}
	// Poll again: approved.
	poll2, err := mgr.Poll(context.Background(), srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if poll2.Status != "ready" {
		t.Fatalf("expected ready, got %s", poll2.Status)
	}
	if srv.TokenJSON == "" {
		t.Fatal("expected persisted token")
	}

	// Token() should return the access token.
	tok, err := mgr.Token(context.Background(), srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "access-token-1" {
		t.Fatalf("expected access-token-1, got %q", tok)
	}
}

func TestOAuthStaticClientIDNoRegistration(t *testing.T) {
	f := newFakeOAuthServer(t)
	defer f.close()
	repo := &mcpRepoForOAuth{
		server: &store.MCPServer{
			ID:              2,
			Name:            "github",
			URL:             f.ts.URL,
			OAuthConfigJSON: `{"client_id":"static-client","client_secret":"","scopes":[]}`,
			Enabled:         true,
		},
	}
	mgr := NewOAuthManager(repo, nil)
	st, err := mgr.Authorize(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "authorizing" {
		t.Fatalf("expected authorizing, got %s", st.Status)
	}
	if f.registrationHits.Load() != 0 {
		t.Fatal("expected no registration for static client_id")
	}
}

func TestOAuthTokenRefresh(t *testing.T) {
	f, mgr, _, srv := newOAuthTestServer(t)
	defer f.close()

	// Persist an expired token manually.
	
	st, err := mgr.Authorize(context.Background(), srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = st
	// Poll to approve.
	if _, err := mgr.Poll(context.Background(), srv.ID); err != nil {
		t.Fatal(err)
	}
	// Force expiry by rewriting the token set.
	srv.TokenJSON = `{"access_token":"old","refresh_token":"refresh-token-1","expires_at":1}`
	if err := repoUpdate(srv, mgr); err != nil {
		t.Fatal(err)
	}

	tok, err := mgr.Token(context.Background(), srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "access-token-2" {
		t.Fatalf("expected refreshed access-token-2, got %q", tok)
	}
	if f.refreshHits.Load() == 0 {
		t.Fatal("expected refresh call")
	}
}

// repoUpdate persists the server row back through the manager's repo.
func repoUpdate(srv *store.MCPServer, mgr *OAuthManager) error {
	return mgr.repo.UpdateMCPServer(context.Background(), srv)
}

func TestOAuthNotAuthorized(t *testing.T) {
	f := newFakeOAuthServer(t)
	defer f.close()
	repo := &mcpRepoForOAuth{
		server: &store.MCPServer{
			ID:              3,
			Name:            "github",
			URL:             f.ts.URL,
			OAuthConfigJSON: `{"client_id":"c1","client_secret":"","scopes":[]}`,
			Enabled:         true,
			TokenJSON:       "",
		},
	}
	mgr := NewOAuthManager(repo, nil)
	_, err := mgr.Token(context.Background(), 3)
	if err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("expected not-authorized error, got %v", err)
	}
}

func TestOAuthNoDeviceEndpoint(t *testing.T) {
	// Server advertising no device_authorization_endpoint.
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server.mcp", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"issuer":             "https://x",
			"token_endpoint":     "https://x/token",
			"registration_endpoint": "https://x/register",
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	repo := &mcpRepoForOAuth{
		server: &store.MCPServer{
			ID:              4,
			Name:            "no-device",
			URL:             ts.URL,
			OAuthConfigJSON: `{"client_id":"c1"}`,
			Enabled:         true,
		},
	}
	mgr := NewOAuthManager(repo, nil)
	_, err := mgr.Authorize(context.Background(), 4)
	if err == nil || !strings.Contains(err.Error(), "device_authorization_endpoint") {
		t.Fatalf("expected no-device-endpoint error, got %v", err)
	}
}

func TestOAuthDeviceCodeAuthorized(t *testing.T) {
	f := newFakeOAuthServer(t)
	defer f.close()
	repo := &mcpRepoForOAuth{
		server: &store.MCPServer{
			ID:              5,
			Name:            "github",
			URL:             f.ts.URL,
			OAuthConfigJSON: `{"client_id":"c1","client_secret":"","scopes":[]}`,
			Enabled:         true,
		},
	}
	mgr := NewOAuthManager(repo, nil)
	st, err := mgr.Authorize(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "authorizing" || st.UserCode == "" {
		t.Fatalf("bad authorize state: %+v", st)
	}
	// Poll with zero pending → approved immediately.
	st2, err := mgr.Poll(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Status != "ready" {
		t.Fatalf("expected ready, got %s", st2.Status)
	}
}

func TestOAuthClear(t *testing.T) {
	f, mgr, _, srv := newOAuthTestServer(t)
	defer f.close()
	if _, err := mgr.Authorize(context.Background(), srv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Poll(context.Background(), srv.ID); err != nil {
		t.Fatal(err)
	}
	if srv.TokenJSON == "" {
		t.Fatal("expected token before clear")
	}
	if err := mgr.Clear(context.Background(), srv.ID); err != nil {
		t.Fatal(err)
	}
	if srv.TokenJSON != "" {
		t.Fatal("expected empty token after clear")
	}
}

func TestOAuthStatus(t *testing.T) {
	f, mgr, _, srv := newOAuthTestServer(t)
	defer f.close()
	st, err := mgr.Status(context.Background(), srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "pending" {
		t.Fatalf("expected pending, got %s", st.Status)
	}
	// After auth.
	if _, err := mgr.Authorize(context.Background(), srv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Poll(context.Background(), srv.ID); err != nil {
		t.Fatal(err)
	}
	st2, _ := mgr.Status(context.Background(), srv.ID)
	if st2.Status != "ready" {
		t.Fatalf("expected ready, got %s", st2.Status)
	}
}

func TestOAuth401RetryWithTokenProvider(t *testing.T) {
	var calls atomicInt64
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fresh-token" {
			t.Fatalf("expected fresh token on retry, got %q", r.Header.Get("Authorization"))
		}
		json.NewEncoder(w).Encode(NewResponse(1, map[string]any{"tools": []Tool{}}))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	tr := &httpTransport{
		baseURL:    ts.URL + "/rpc",
		httpClient: ts.Client(),
		tokenProvider: func(ctx context.Context) (string, error) {
			return "fresh-token", nil
		},
	}
	c := NewClientWithTransport(tr)
	if _, err := c.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 calls (401 + retry), got %d", calls.Load())
	}
}

func TestParseDeviceCodeInterval(t *testing.T) {
	if got := parseDeviceCodeInterval("3"); got != 3 {
		t.Fatalf("expected 3, got %d", got)
	}
	if got := parseDeviceCodeInterval("abc"); got != 5 {
		t.Fatalf("expected default 5, got %d", got)
	}
	if got := parseDeviceCodeInterval("0"); got != 5 {
		t.Fatalf("expected default 5 for 0, got %d", got)
	}
}
