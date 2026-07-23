package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"

	"github.com/sn0wfree/llmRx/internal/config"
)

// TestStart_GracefulShutdownDrainsInflight verifies that when the
// context is cancelled mid-request, the server keeps the
// in-flight request alive until it finishes, then exits cleanly.
func TestStart_GracefulShutdownDrainsInflight(t *testing.T) {
	s := &Server{
		cfg: &config.Config{Server: config.ServerConfig{Port: 0, Host: "127.0.0.1"}},
	}
	s.engine = chi.NewRouter()

	var started, finished sync.WaitGroup
	started.Add(1)
	finished.Add(1)
	s.engine.Get("/slow", func(w http.ResponseWriter, r *http.Request) {
		started.Done()
		// Hold the request long enough that the shutdown context
		// fires before the handler returns. If Shutdown cancels
		// r.Context() before the handler exits, this select will
		// pick the cancellation branch and fail.
		select {
		case <-time.After(300 * time.Millisecond):
		case <-r.Context().Done():
			t.Errorf("request was cancelled by Shutdown instead of draining")
		}
		finished.Done()
		_, _ = io.WriteString(w, "ok")
	})

	// Pick a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	s.cfg.Server.Port = mustPort(t, addr)

	ctx, cancel := context.WithCancel(context.Background())

	startErr := make(chan error, 1)
	go func() { startErr <- s.Start(ctx) }()

	if !waitListening(addr, 2*time.Second) {
		cancel()
		t.Fatal("listener never came up")
	}

	// Fire the slow request.
	type result struct {
		body string
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/slow")
		if err != nil {
			resCh <- result{err: err}
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		resCh <- result{body: string(body)}
	}()

	// Wait for the handler to enter the critical section.
	waitOrFail(t, &started, 1*time.Second)

	// Trigger shutdown while the handler is mid-flight.
	cancel()

	// Start() should return nil (clean shutdown).
	select {
	case err := <-startErr:
		if err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return within 5s of cancellation")
	}

	// The in-flight request should have completed successfully.
	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("in-flight request errored: %v", r.err)
		}
		if r.body != "ok" {
			t.Fatalf("in-flight body: %q", r.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request never returned")
	}
	if !waitOrFail(t, &finished, 500*time.Millisecond) {
		t.Fatal("handler did not finish — Shutdown cancelled it instead of draining")
	}
}

// TestStart_ListenErrorSurfaces: a misconfigured address (port
// already in use) should make Start return the listen error
// rather than silently hanging.
func TestStart_ListenErrorSurfaces(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold listen: %v", err)
	}
	defer ln.Close()
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	s := &Server{cfg: &config.Config{Server: config.ServerConfig{Host: host, Port: mustParsePort(t, port)}}}
	s.engine = chi.NewRouter()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startErr := make(chan error, 1)
	go func() { startErr <- s.Start(ctx) }()

	select {
	case err := <-startErr:
		if err == nil {
			t.Fatal("expected listen error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not surface listen error")
	}
}

func waitListening(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// waitOrFail returns true if the WaitGroup finishes within the
// timeout. False is a non-fatal "still pending" signal — the
// caller decides whether to t.Fatal.
func waitOrFail(t *testing.T, wg *sync.WaitGroup, timeout time.Duration) bool {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func mustPort(t *testing.T, addr string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	return mustParsePort(t, port)
}

func mustParsePort(t *testing.T, s string) int {
	t.Helper()
	var p int
	if _, err := fmt.Sscanf(s, "%d", &p); err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return p
}

var _ = httptest.NewRecorder

// TestCORSOptions_DefaultIsNoOrigins: with no CORSAllowedOrigins
// the helper must return a zero-value AllowedOrigins. The caller
// (registerMiddleware) skips installing the middleware in this
// case so no Access-Control-Allow-Origin header is emitted.
func TestCORSOptions_DefaultIsNoOrigins(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	opts := s.corsOptions()
	if len(opts.AllowedOrigins) != 0 {
		t.Fatalf("default AllowedOrigins must be empty, got %v", opts.AllowedOrigins)
	}
	if containsAny(opts.AllowedOrigins, "*") {
		t.Fatalf("default AllowedOrigins must NOT include '*', got %v", opts.AllowedOrigins)
	}
}

// TestCORSOptions_OperatorPinsOrigins: configured origins are
// echoed verbatim into AllowedOrigins.
func TestCORSOptions_OperatorPinsOrigins(t *testing.T) {
	s := &Server{cfg: &config.Config{Server: config.ServerConfig{
		CORSAllowedOrigins: []string{"https://app.example.com", "https://admin.example.com"},
	}}}
	opts := s.corsOptions()
	if !equalStringSlice(opts.AllowedOrigins, []string{"https://app.example.com", "https://admin.example.com"}) {
		t.Fatalf("AllowedOrigins = %v", opts.AllowedOrigins)
	}
}

// TestCORSOptions_LegacyWildcardOptIn: "*" survives the
// config round-trip when the operator explicitly sets it (dev
// workflow); we don't silently expand to it.
func TestCORSOptions_LegacyWildcardOptIn(t *testing.T) {
	s := &Server{cfg: &config.Config{Server: config.ServerConfig{
		CORSAllowedOrigins: []string{"*"},
	}}}
	opts := s.corsOptions()
	if !containsAny(opts.AllowedOrigins, "*") {
		t.Fatalf("explicit '*' must be preserved when configured, got %v", opts.AllowedOrigins)
	}
}

// TestCORS_NoACAOHeaderByDefault: a preflight OPTIONS request
// against a server with no configured origins must NOT receive
// an Access-Control-Allow-Origin header. This is the safe
// behaviour for a server-to-server gateway.
func TestCORS_NoACAOHeaderByDefault(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	s.engine = chi.NewRouter()
	// Mirror registerMiddleware: only install cors when configured.
	if len(s.cfg.Server.CORSAllowedOrigins) > 0 {
		s.engine.Use(cors.Handler(s.corsOptions()))
	}
	s.engine.Get("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodOptions, "/v1/models", nil)
	r.Header.Set("Origin", "https://attacker.example")
	r.Header.Set("Access-Control-Request-Method", "GET")
	s.engine.ServeHTTP(rec, r)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("ACAO header should be empty, got %q", got)
	}
}

// TestCORS_AllowedOriginEchoed: configured origins are echoed
// back on a preflight OPTIONS request.
func TestCORS_AllowedOriginEchoed(t *testing.T) {
	s := &Server{cfg: &config.Config{Server: config.ServerConfig{
		CORSAllowedOrigins: []string{"https://app.example.com"},
	}}}
	s.engine = chi.NewRouter()
	s.engine.Use(cors.Handler(s.corsOptions()))
	s.engine.Get("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodOptions, "/v1/models", nil)
	r.Header.Set("Origin", "https://app.example.com")
	r.Header.Set("Access-Control-Request-Method", "GET")
	s.engine.ServeHTTP(rec, r)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("ACAO header = %q, want %q", got, "https://app.example.com")
	}
}

// helpers for slice checks

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsAny(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

// silence unused-import for strings in case future edits drop
// the helpers above
var _ = strings.Contains