package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestNew_ConstructsServer(t *testing.T) {
	app := testhelper.New(t)
	cfg := &config.Config{Server: config.ServerConfig{Port: 0}}
	s := New(cfg, "config.yml", app.Engine, app.Pool, app.Store, app.LogStore, app.Cache, app.LogBroker, app.RT, "")
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
	s := New(cfg, "config.yml", app.Engine, app.Pool, app.Store, app.LogStore, app.Cache, app.LogBroker, app.RT, "")

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
	s := New(cfg, "config.yml", app.Engine, app.Pool, app.Store, app.LogStore, app.Cache, app.LogBroker, app.RT, "")

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
	s := New(cfg, "config.yml", app.Engine, app.Pool, app.Store, app.LogStore, app.Cache, app.LogBroker, app.RT, "")

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
	s := New(cfg, "config.yml", app.Engine, app.Pool, app.Store, app.LogStore, app.Cache, app.LogBroker, app.RT, "")
	s.SetAlertManager(nil)
}

func TestRegisterMiddleware_NoCORSByDefault(t *testing.T) {
	app := testhelper.New(t)
	cfg := &config.Config{Server: config.ServerConfig{Port: 0}}
	s := New(cfg, "config.yml", app.Engine, app.Pool, app.Store, app.LogStore, app.Cache, app.LogBroker, app.RT, "")

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
	s := New(cfg, "config.yml", app.Engine, app.Pool, app.Store, app.LogStore, app.Cache, app.LogBroker, app.RT, "")

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
