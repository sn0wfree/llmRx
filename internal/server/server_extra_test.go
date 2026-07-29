package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestNew_ConstructsServer(t *testing.T) {
	app := testhelper.New(t)
	cfg := &config.Config{Server: config.ServerConfig{Port: 0}}
	s := New(cfg, "config.yml", app.Engine, app.Pool, app.Store, app.LogStore, app.Cache, app.LogBroker, app.RT, "", nil)
	if s == nil {
		t.Fatal("New returned nil")
	}
	if s.engine == nil {
		t.Fatal("engine not initialized")
	}
	if s.admin == nil {
		t.Fatal("admin handler not initialized")
	}
}

func TestNew_HealthEndpoint(t *testing.T) {
	app := testhelper.New(t)
	cfg := &config.Config{Server: config.ServerConfig{Port: 0}}
	s := New(cfg, "config.yml", app.Engine, app.Pool, app.Store, app.LogStore, app.Cache, app.LogBroker, app.RT, "", nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	s.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("health: expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !containsStr(body, "ok") {
		t.Fatalf("health body should contain 'ok': %s", body)
	}
	if !containsStr(body, "intent_backend") {
		t.Fatalf("health body should contain intent_backend: %s", body)
	}
}

func TestNew_AdminAPIMounted(t *testing.T) {
	app := testhelper.New(t)
	cfg := &config.Config{Server: config.ServerConfig{Port: 0}}
	s := New(cfg, "config.yml", app.Engine, app.Pool, app.Store, app.LogStore, app.Cache, app.LogBroker, app.RT, "", nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/v1/channels", nil)
	s.engine.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatal("/admin/api/v1 not mounted")
	}
}

func TestNew_V1Mounted(t *testing.T) {
	app := testhelper.New(t)
	cfg := &config.Config{Server: config.ServerConfig{Port: 0}}
	s := New(cfg, "config.yml", app.Engine, app.Pool, app.Store, app.LogStore, app.Cache, app.LogBroker, app.RT, "", nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	s.engine.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatal("/v1 not mounted")
	}
}

func TestSetAlertManager_NilAdmin(t *testing.T) {
	s := &Server{}
	s.SetAlertManager(nil)
}

func TestSetAlertManager_WithAdmin(t *testing.T) {
	app := testhelper.New(t)
	cfg := &config.Config{Server: config.ServerConfig{Port: 0}}
	s := New(cfg, "config.yml", app.Engine, app.Pool, app.Store, app.LogStore, app.Cache, app.LogBroker, app.RT, "", nil)
	s.SetAlertManager(nil)
}

func TestRegisterMiddleware_NoCORSByDefault(t *testing.T) {
	app := testhelper.New(t)
	cfg := &config.Config{Server: config.ServerConfig{Port: 0}}
	s := New(cfg, "config.yml", app.Engine, app.Pool, app.Store, app.LogStore, app.Cache, app.LogBroker, app.RT, "", nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/health", nil)
	req.Header.Set("Origin", "https://evil.example")
	s.engine.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("no CORS by default, got ACAO=%q", got)
	}
}

func TestRegisterMiddleware_WithCORS(t *testing.T) {
	app := testhelper.New(t)
	cfg := &config.Config{Server: config.ServerConfig{
		Port:               0,
		CORSAllowedOrigins: []string{"https://trusted.example"},
	}}
	s := New(cfg, "config.yml", app.Engine, app.Pool, app.Store, app.LogStore, app.Cache, app.LogBroker, app.RT, "", nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/health", nil)
	req.Header.Set("Origin", "https://trusted.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	s.engine.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://trusted.example" {
		t.Fatalf("CORS ACAO mismatch: got %q", got)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestNew_BYOKHookWired covers the regression where a setter
// (formerly SetBYOKHook) silently never fired because the
// middleware chain is registered before New returns. Now that
// the hook is a New parameter, this test pins the contract:
// unknown sk- bearers actually reach the hook.
func TestNew_BYOKHookWired(t *testing.T) {
	app := testhelper.New(t)
	cfg := &config.Config{Server: config.ServerConfig{Port: 0}}

	called := false
	hook := func(w http.ResponseWriter, r *http.Request, rawKey string) {
		called = true
		w.Header().Set("X-BYOK-Key", rawKey)
		w.WriteHeader(http.StatusTeapot)
	}
	s := New(cfg, "config.yml", app.Engine, app.Pool, app.Store, app.LogStore, app.Cache, app.LogBroker, app.RT, "", hook)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer sk-new-bearer-unknown-to-cache")
	req.RemoteAddr = "10.0.0.1:54321"
	s.engine.ServeHTTP(rec, req)

	if !called {
		t.Fatal("byok hook was not invoked; New parameter is not wired")
	}
	if rec.Header().Get("X-BYOK-Key") != "sk-new-bearer-unknown-to-cache" {
		t.Fatalf("hook received wrong key: %q", rec.Header().Get("X-BYOK-Key"))
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("hook should fully own the response, got code=%d", rec.Code)
	}
}

// TestNew_NoBYOKHook_Gives403 covers the case where the hook
// is nil: the middleware chain must still 403 unknown tokens
// (the same behaviour the legacy WithLimits provided). With
// byokHook=nil the BYOK feature is effectively off.
func TestNew_NoBYOKHook_Gives403(t *testing.T) {
	app := testhelper.New(t)
	cfg := &config.Config{Server: config.ServerConfig{Port: 0}}
	s := New(cfg, "config.yml", app.Engine, app.Pool, app.Store, app.LogStore, app.Cache, app.LogBroker, app.RT, "", nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer sk-unknown-bearer")
	s.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 invalid_token without hook, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error.Code != "invalid_token" {
		t.Fatalf("expected code=invalid_token, got %q", body.Error.Code)
	}
}
