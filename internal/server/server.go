package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/sn0wfree/llmRx/internal/admin"
	"github.com/sn0wfree/llmRx/internal/alert"
	"github.com/sn0wfree/llmRx/internal/api"
	"github.com/sn0wfree/llmRx/internal/broker"
	"github.com/sn0wfree/llmRx/internal/config"
	authmw "github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/runtime"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/tokencache"
	"github.com/sn0wfree/llmRx/internal/observability"
	"github.com/sn0wfree/llmRx/internal/webui"
)

type Server struct {
	cfg        *config.Config
	cfgPath    string
	keyFile    string
	router     *router.RouterEngine
	pool       *pool.ChannelPool
	store      store.Store
	tokens     *tokencache.Cache
	admin      *admin.Handler
	engine     *chi.Mux
	httpServer *http.Server
}

func New(cfg *config.Config, cfgPath string, eng *router.RouterEngine, cp *pool.ChannelPool, st store.Store, tc *tokencache.Cache, lb *broker.Broker[*model.Log], rt *runtime.Defaults, keyFile string) *Server {
	s := &Server{
		cfg:     cfg,
		cfgPath: cfgPath,
		keyFile: keyFile,
		router:  eng,
		pool:    cp,
		store:   st,
		tokens:  tc,
		engine:  chi.NewRouter(),
	}
	s.registerMiddleware()
	s.registerRoutes(lb, rt)
	return s
}

func (s *Server) registerMiddleware() {
	s.engine.Use(chimw.Logger)
	s.engine.Use(chimw.Recoverer)
	s.engine.Use(chimw.RealIP)
	s.engine.Use(chimw.Timeout(120 * time.Second))
	// chi/cors defaults to AllowedOrigins=* when the slice is
	// empty, so we skip the middleware entirely when no origins
	// are configured. This matches the safe default for a
	// server-to-server gateway: no Access-Control-Allow-Origin
	// header is emitted at all and browsers reject cross-origin
	// requests before they reach our handlers.
	if len(s.cfg.Server.CORSAllowedOrigins) > 0 {
		s.engine.Use(cors.Handler(s.corsOptions()))
	}
}

// corsOptions returns the configured CORS policy.
func (s *Server) corsOptions() cors.Options {
	return cors.Options{
		AllowedOrigins:   s.cfg.Server.CORSAllowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type", "X-Task-Type", "X-Session-Token"},
		ExposedHeaders:   []string{"X-Session-Token"},
		AllowCredentials: false,
		MaxAge:           300,
	}
}

func (s *Server) registerRoutes(lb *broker.Broker[*model.Log], rt *runtime.Defaults) {
	handler := api.New(s.cfg, s.router, s.pool, s.store, lb, rt)
	adminHandler := admin.New(s.store, s.pool, s.router, s.tokens, lb, rt, s.cfg, s.keyFile)
	s.admin = adminHandler

	s.engine.With(authmw.WithLimits(s.tokens.Lookup, handler.Limits())).
		Mount("/v1", handler.Routes())

	s.engine.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","intent_backend":%q}`, s.router.IntentBackend())
	})

	// Phase 0: html/template admin UI. Legacy JSON API still
	// available under /admin/api/v1 for backwards compatibility.
	webAPIBridge := webui.NewWebAPIBridge(s.store)
	webAPIBridge.SetReloader(func() error {
		if err := s.tokens.Reload(); err != nil {
			return err
		}
		return s.pool.LoadFromStore(s.store)
	})
	webUI, err := webui.New(s.store, webAPIBridge, s.cfgPath)
	if err != nil {
		log.Fatalf("webui: %v", err)
	}
	s.engine.Mount("/admin", webUI.Routes())
	s.engine.Mount("/admin/api/v1", adminHandler.Routes())
}

// Start blocks running the HTTP listener until ctx is cancelled.
// On cancellation it shuts the server down with a graceful
// timeout so in-flight requests (chat completions, log writes,
// plan spend transactions) get a chance to finish before the
// process exits. K8s sends SIGTERM with a default 30s
// terminationGracePeriod; the timeout below matches that
// comfortably so the kubelet doesn't have to SIGKILL.
func (s *Server) Start(ctx context.Context) error {
	host := s.cfg.Server.Host
	if host == "" {
		host = "0.0.0.0"
	}
	addr := fmt.Sprintf("%s:%d", host, s.cfg.Server.Port)
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           s.engine,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       0, // unbounded body read; chi middleware applies timeouts per route
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}
	if s.tokens != nil {
		log.Printf("listening on %s (tokens=%d)", addr, s.tokens.Size())
	} else {
		log.Printf("listening on %s", addr)
	}

	errCh := make(chan error, 1)
	go func() {
		err := s.httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Printf("server: shutdown signal received, draining (timeout=25s)...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		log.Printf("server: stopped cleanly")
		return nil
	}
}

// SetAlertManager injects the alert manager into the admin handler
// so that POST /api/v1/reload can also refresh alert rules.
func (s *Server) SetAlertManager(m *alert.Manager) {
	if s.admin != nil {
		s.admin.SetAlertManager(m)
	}
}

// StartMetricsServer starts the Prometheus metrics server on the
// configured MetricsAddr. Returns nil if metrics are disabled.
func (s *Server) StartMetricsServer(ctx context.Context) func() {
	addr := s.cfg.Server.MetricsAddr
	if addr == "" {
		return func() {}
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", observability.Handler())

	// Optional auth token.
	token := s.cfg.Server.MetricsAuthToken
	if token != "" {
		mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			observability.Handler().ServeHTTP(w, r)
		})
	}

	ms := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("metrics: listening on %s", addr)
		if err := ms.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("metrics: %v", err)
		}
	}()

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ms.Shutdown(shutdownCtx)
	}
}