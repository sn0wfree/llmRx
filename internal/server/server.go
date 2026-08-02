package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/sn0wfree/llmRx/internal/dialect"
	"github.com/sn0wfree/llmRx/internal/guardrail"
	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/mcp"
	authmw "github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/observability"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/prober"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/ratelimit"
	"github.com/sn0wfree/llmRx/internal/requestid"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/runtime"
	"github.com/sn0wfree/llmRx/internal/secrets"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/tokencache"
	"github.com/sn0wfree/llmRx/internal/webui"
)

// fatalf is the structured-replacement for log.Fatalf in startup
// paths that exit the process. Mirrors cmd/gateway/main.go.
func fatalf(msg string, fields ...logging.Field) {
	logging.Error(msg, fields...)
	os.Exit(1)
}

type Server struct {
	cfg          *config.Config
	cfgPath      string
	keyFile      string
	router       *router.RouterEngine
	pool         *pool.ChannelPool
	store        store.Store
	logStore     *logstore.Manager
	tokens       *tokencache.Cache
	admin        *admin.Handler
	engine       *chi.Mux
	httpServer   *http.Server
	prober       *prober.Cache
	mcpClientMgr *mcp.ClientManager
	mcpServer    *mcp.Server
	// limiter overrides the api handler's default process-local
	// limiter (cluster-shared PG window backend, P12 M2).
	limiter *ratelimit.Limiter
	// guardrailEngine is the shared rule cache; reloaded on config
	// propagation (ReloadConfig) and admin writes.
	guardrailEngine *guardrail.GuardrailEngine
	// alertMgr is set via SetAlertManager; reloaded on propagation.
	alertMgr *alert.Manager
	// notifyFn broadcasts local config writes to other replicas
	// (PG NOTIFY). nil = no-op (single node).
	notifyFn func()
	// byokHook, when set, is invoked on unknown bearer tokens
	// so the BYOK manager can probe+register them. nil means
	// strict 403 (legacy behaviour).
	byokHook authmw.UnknownTokenHook
}

// New wires the HTTP router. byokHook (optional) is invoked
// when the token cache misses — typically a *byok.Manager.Hook.
// Must be supplied at construction time (not via setter)
// because the middleware chain is registered before New
// returns; a setter would silently never be wired.
// lim (optional) is a limiter with a cluster-shared window backend;
// nil uses the default process-local limiter.
func New(cfg *config.Config, cfgPath string, eng *router.RouterEngine, cp *pool.ChannelPool, st store.Store, ls *logstore.Manager, tc *tokencache.Cache, lb *broker.Broker[*model.Log], rt *runtime.Defaults, keyFile string, byokHook authmw.UnknownTokenHook, lim ...*ratelimit.Limiter) *Server {
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
	if len(lim) > 0 && lim[0] != nil {
		s.limiter = lim[0]
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
	if s.limiter != nil {
		handler.SetLimiter(s.limiter)
	}

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

	// Initialize MCP components.
	s.mcpClientMgr = mcp.NewClientManager(s.store)
	var oauthSec *secrets.Manager
	if sp, ok := s.store.(store.SecretsProvider); ok {
		oauthSec = sp.SecretsManager()
	}
	oauthMgr := mcp.NewOAuthManager(s.store, oauthSec)
	s.mcpClientMgr.SetOAuthManager(oauthMgr)
	mcpHandler := mcpToolHandler(s.pool, s.router)
	s.mcpServer = mcp.NewServer(mcp.DefaultTools(), mcpHandler)
	mcpLoop := mcp.NewAgenticLoop(s.mcpClientMgr, s.store, provider.NewOpenAIProvider())
	handler.SetMCPLoop(mcpLoop)

	adminHandler := admin.New(s.store, s.logStore, s.pool, s.router, s.tokens, lb, rt, s.cfg, s.keyFile)
	s.admin = adminHandler
	if responseCache != nil {
		adminHandler.SetCache(responseCache)
	}
	adminHandler.SetMCPClientManager(s.mcpClientMgr)
	adminHandler.SetGuardrailEngine(handler.GuardrailEngine())
	s.guardrailEngine = handler.GuardrailEngine()
	adminHandler.SetReloadNotifier(s.fireNotify)

	// WithLimitsAndOptions (vs. WithLimits) is what actually
	// fires the BYOK hook when an unknown bearer arrives. If
	// s.byokHook is nil we fall back to the strict 403 path —
	// same effective behaviour as the old WithLimits call.
	s.engine.With(authmw.WithLimitsAndOptions(s.tokens.Lookup, handler.Limits(), s.byokHook)).
		Mount("/v1", handler.Routes())

	// MCP server endpoint: expose llmRx's own tools.
	s.engine.Post("/mcp/llmrx", func(w http.ResponseWriter, r *http.Request) {
		body, err := readBody(r)
		if err != nil {
			http.Error(w, `{"jsonrpc":"2.0","error":{"code":-32700,"message":"invalid body"}}`, http.StatusBadRequest)
			return
		}
		resp := s.mcpServer.HandleHTTP(r.Context(), body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

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
		s.fireNotify()
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
	webUI.SetMCPClientManager(s.mcpClientMgr)
	webUI.SetOAuthManager(oauthMgr)
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
			logging.Warn("cache: db backend requested but store has no RawDB")
			return nil
		}
		// P12 M3: the response cache lives in the store's own
		// database — with a Postgres store, replicas share one
		// response_cache table (cluster-wide hit rate). The
		// dialect follows the store driver automatically, so no
		// SQLite-on-Postgres mismatch is possible.
		d := dialect.Dialect(dialect.SQLite{})
		if s.cfg.Database.Driver == "postgres" {
			d = dialect.Postgres{}
		}
		c, err := cache.NewDBCache(rawDB, d)
		if err != nil {
			logging.Warn("cache: db backend init failed", logging.F("error", err.Error()))
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

// readBody reads the full request body, capped at 1 MiB.
func readBody(r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	b, err := io.ReadAll(r.Body)
	r.Body.Close()
	return b, err
}

func mcpToolHandler(pool *pool.ChannelPool, eng *router.RouterEngine) mcp.ToolHandler {
	return func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		switch name {
		case "channel_list":
			channels := pool.GetAllChannels()
			type chView struct {
				ID     int64    `json:"id"`
				Name   string   `json:"name"`
				Models []string `json:"models"`
				Status int      `json:"status"`
			}
			views := make([]chView, 0, len(channels))
			for _, ch := range channels {
				views = append(views, chView{
					ID:     ch.ID,
					Name:   ch.Name,
					Models: ch.Models,
					Status: int(ch.Status),
				})
			}
			return map[string]any{"channels": views}, nil
		case "channel_invoke":
			model, _ := args["model"].(string)
			prompt, _ := args["prompt"].(string)
			system, _ := args["system"].(string)
			if model == "" || prompt == "" {
				return nil, fmt.Errorf("model and prompt are required")
			}
			messages := []provider.Message{
				{Role: "user", Content: prompt},
			}
			if system != "" {
				messages = append([]provider.Message{{Role: "system", Content: system}}, messages...)
			}
			chatReq := &provider.ChatRequest{
				Model:    model,
				Messages: messages,
			}
			route, err := eng.RouteWith(ctx, model, router.RouteOptions{Text: prompt})
			if err != nil {
				return nil, fmt.Errorf("no available channel: %v", err)
			}
			prov := provider.NewOpenAIProvider()
			resp, statusCode, err := prov.Chat(ctx, chatReq, route.KeyValue, route.Channel.BaseURL)
			if err != nil {
				return nil, fmt.Errorf("upstream error (status %d): %v", statusCode, err)
			}
			text := ""
			if len(resp.Choices) > 0 {
				text = resp.Choices[0].Message.ContentString()
			}
			return mcp.MarshalToolResult(text, false), nil
		default:
			return nil, fmt.Errorf("unknown tool: %s", name)
		}
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
	s.alertMgr = m
	if s.admin != nil {
		s.admin.SetAlertManager(m)
	}
}

// SetReloadNotifier installs the cross-replica notify sender
// (PG NOTIFY llmrx_reload). Called on local admin writes so other
// replicas reload promptly; nil (default) is a no-op.
func (s *Server) SetReloadNotifier(fn func()) { s.notifyFn = fn }

// fireNotify broadcasts a reload message to other replicas. Safe
// to call on every admin write path.
func (s *Server) fireNotify() {
	if s.notifyFn != nil {
		s.notifyFn()
	}
}

// ReloadConfig refreshes the process-local caches that mirror
// database state, so admin writes made on other replicas take
// effect here. Driven by the PG LISTEN/NOTIFY reload channel plus
// a 30s polling fallback (P12 M2 config propagation). Router
// breaker/Thompson posterior state is intentionally left alone —
// it is per-node observation and resets would lose signal.
func (s *Server) ReloadConfig() {
	if err := s.tokens.Reload(); err != nil {
		logging.Warn("config reload: tokencache", logging.F("error", err.Error()))
	}
	if s.guardrailEngine != nil {
		if err := s.guardrailEngine.Reload(); err != nil {
			logging.Warn("config reload: guardrails", logging.F("error", err.Error()))
		}
	}
	if s.alertMgr != nil {
		if err := s.alertMgr.Reload(); err != nil {
			logging.Warn("config reload: alerts", logging.F("error", err.Error()))
		}
	}
	if s.router != nil {
		s.router.ReloadAllChannels()
	}
	if s.pool != nil {
		if err := s.pool.LoadFromStore(s.store); err != nil {
			logging.Warn("config reload: pool", logging.F("error", err.Error()))
		}
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
