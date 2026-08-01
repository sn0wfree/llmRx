package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/sn0wfree/llmRx/internal/admin"
	"github.com/sn0wfree/llmRx/internal/alert"
	"github.com/sn0wfree/llmRx/internal/api"
	"github.com/sn0wfree/llmRx/internal/broker"
	"github.com/sn0wfree/llmRx/internal/cache"
	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/logstore"
	authmw "github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/prober"
	"github.com/sn0wfree/llmRx/internal/requestid"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/runtime"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/tokencache"
	"github.com/sn0wfree/llmRx/internal/observability"
	"github.com/sn0wfree/llmRx/internal/webui"
)

// fatalf is the structured-replacement for log.Fatalf in startup
// paths that exit the process. Mirrors cmd/gateway/main.go.
func fatalf(msg string, fields ...logging.Field) {
	logging.Error(msg, fields...)
	os.Exit(1)
}

type Server struct {
	cfg        *config.Config
	cfgPath    string
	keyFile    string
	router     *router.RouterEngine
	pool       *pool.ChannelPool
	store      store.Store
	logStore   *logstore.Manager
	tokens     *tokencache.Cache
	admin      *admin.Handler
	engine     *chi.Mux
	httpServer *http.Server
	prober     *prober.Cache
	// byokHook, when set, is invoked on unknown bearer tokens
	// so the BYOK manager can probe+register them. nil means
	// strict 403 (legacy behaviour).
	byokHook   authmw.UnknownTokenHook
}

// New wires the HTTP router. byokHook (optional) is invoked
// when the token cache misses — typically a *byok.Manager.Hook.
// Must be supplied at construction time (not via setter)
// because the middleware chain is registered before New
// returns; a setter would silently never be wired.
func New(cfg *config.Config, cfgPath string, eng *router.RouterEngine, cp *pool.ChannelPool, st store.Store, ls *logstore.Manager, tc *tokencache.Cache, lb *broker.Broker[*model.Log], rt *runtime.Defaults, keyFile string, byokHook authmw.UnknownTokenHook) *Server {
	s := &Server{
		cfg:      cfg,
		cfgPath:  cfgPath,
		keyFile:  keyFile,
		router:   eng,
		pool:     cp,
		store:    st,
		logStore: ls,
		tokens:   tc,
		byokHook: byokHook,
		engine:   chi.NewRouter(),
	}
	s.registerMiddleware()
	s.registerRoutes(lb, rt)
	return s
}

func (s *Server) registerMiddleware() {
	s.engine.Use(requestid.Middleware)
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
	handler := api.New(s.cfg, s.router, s.pool, s.store, s.logStore, lb, rt)

	// Initialize response cache.
	responseCache := s.initResponseCache()
	if responseCache != nil {
		handler.SetCache(responseCache)
		logging.Info("response cache initialized",
			logging.F("backend", s.cfg.Server.CacheBackend),
			logging.F("ttl_sec", s.cfg.Server.CacheTTLSeconds),
		)
	}
	proberCache := prober.New(prober.Config{}, s.store, s.pool)
	handler.SetProber(proberCache)
	s.prober = proberCache
	// Wire real-traffic signals into the routing engine. Every
	// successful/failed real call now updates the probe cache so
	// busy channels get zero probe requests (real traffic is a
	// better health signal than a synthetic /v1/models probe).
	s.router.SetTrafficObserver(prober.NewRouterObserver(proberCache))
	logging.Info("prober: wired into router (real-traffic observer)")
	adminHandler := admin.New(s.store, s.logStore, s.pool, s.router, s.tokens, lb, rt, s.cfg, s.keyFile)
	s.admin = adminHandler
	if responseCache != nil {
		adminHandler.SetCache(responseCache)
	}

	// WithLimitsAndOptions (vs. WithLimits) is what actually
	// fires the BYOK hook when an unknown bearer arrives. If
	// s.byokHook is nil we fall back to the strict 403 path —
	// same effective behaviour as the old WithLimits call.
	s.engine.With(authmw.WithLimitsAndOptions(s.tokens.Lookup, handler.Limits(), s.byokHook)).
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
	webUI, err := webui.New(s.store, s.logStore, webAPIBridge, s.cfgPath)
	if err != nil {
		fatalf("webui init failed", logging.F("error", err.Error()))
	}
	webUI.SetProber(proberCache)
	if responseCache != nil {
		webUI.SetCache(responseCache)
	}
	s.engine.Mount("/admin", webUI.Routes())
	s.engine.Mount("/admin/api/v1", adminHandler.Routes())
}

// initResponseCache creates the cache backend based on the server
// config. Returns nil when no backend is configured or when the
// backend type is unknown (logged as a warning).
func (s *Server) initResponseCache() cache.Cache {
	switch s.cfg.Server.CacheBackend {
	case "memory":
		maxItems := s.cfg.Server.CacheMaxItems
		if maxItems <= 0 {
			maxItems = 10000
		}
		return cache.NewMemoryCache(maxItems)
	case "sqlite":
		rawDB := s.store.RawDB()
		if rawDB == nil {
			logging.Warn("cache: sqlite backend requested but store has no RawDB")
			return nil
		}
		c, err := cache.NewSQLiteCache(rawDB)
		if err != nil {
			logging.Warn("cache: sqlite backend init failed", logging.F("error", err.Error()))
			return nil
		}
		return c
	case "":
		return nil
	default:
		logging.Warn("cache: unknown backend", logging.F("backend", s.cfg.Server.CacheBackend))
		return nil
	}
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
		logging.Info("listening", logging.F("addr", addr), logging.F("tokens", s.tokens.Size()))
	} else {
		logging.Info("listening", logging.F("addr", addr))
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
		logging.Info("server shutdown signal received, draining",
		logging.F("timeout_s", 25),
	)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if s.prober != nil {
			s.prober.Stop()
		}
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		logging.Info("server stopped cleanly")
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

	// Optional auth token. Registered unconditionally so the same
	// mux path is used regardless of whether auth is enabled.
	token := s.cfg.Server.MetricsAuthToken
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		observability.Handler().ServeHTTP(w, r)
	})

	ms := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logging.Info("metrics listening", logging.F("addr", addr))
		if err := ms.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logging.Warn("metrics server error", logging.F("error", err.Error()))
		}
	}()

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ms.Shutdown(shutdownCtx)
	}
}