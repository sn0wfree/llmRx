package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/sn0wfree/llmRx/internal/admin"
	"github.com/sn0wfree/llmRx/internal/alert"
	"github.com/sn0wfree/llmRx/internal/api"
	"github.com/sn0wfree/llmRx/internal/balance"
	"github.com/sn0wfree/llmRx/internal/broker"
	"github.com/sn0wfree/llmRx/internal/cache"
	"github.com/sn0wfree/llmRx/internal/clientapi"
	"github.com/sn0wfree/llmRx/internal/cluster"
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
	"github.com/sn0wfree/llmRx/internal/router/session"
	"github.com/sn0wfree/llmRx/internal/runtime"
	"github.com/sn0wfree/llmRx/internal/secrets"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/subusage"
	"github.com/sn0wfree/llmRx/internal/tokencache"
	"github.com/sn0wfree/llmRx/internal/usage"
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
	sessions     *session.Store
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
	// clusterManager manages cluster nodes in multi-node mode.
	clusterManager *cluster.Manager
	// buildVersion is reported by GET /health and the X-Llmrx-Version
	// response header. Set via SetBuildVersion; empty means "dev".
	buildVersion string
	// balanceMon refreshes upstream account balances (P14). nil leaves
	// the balance endpoints returning 503.
	balanceMon *balance.Monitor
	// usageMon polls vendor-side subscription usage (P22).
	usageMon *subusage.Monitor
	// webUI is kept so setters called AFTER New() (main builds the
	// monitors once the store is ready) can still reach the HTML
	// handler. Route registration happens in New, so without this the
	// subscription page would never see its monitor.
	webUI *webui.Handler
	// edgeIngestor owns the /client/v1 surface and the control-plane
	// state (P15 phase 2). nil when no edge clients are configured.
	edgeIngestor *clientapi.Ingestor
}

// SetBuildVersion records the build identity so /health and the
// version header can report it. Injected from cmd/gateway, which owns
// the ldflags-injected values; the server package cannot see main's
// variables. Call before the listener starts — the value is read-only
// afterwards.
func (s *Server) SetBuildVersion(v string) { s.buildVersion = v }

// buildVersionOrDev returns a non-empty version string for responses.
func (s *Server) buildVersionOrDev() string {
	if s.buildVersion == "" {
		return "dev"
	}
	return s.buildVersion
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
	// Stamp the build version on every response (including errors and
	// SSE) so a client can tell which gateway answered without a
	// separate health probe. Registered first so it wraps everything
	// downstream, including the recoverer's 500 responses.
	//
	// The version is read per request, not captured here: middleware is
	// registered during New(), while SetBuildVersion runs afterwards, so
	// capturing the value now would freeze it at "dev".
	s.engine.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Llmrx-Version", s.buildVersionOrDev())
			next.ServeHTTP(w, r)
		})
	})
	s.engine.Use(requestid.Middleware)
	s.engine.Use(noStore)
	s.engine.Use(requestLogger)
	s.engine.Use(cluster.MetricsMiddleware)
	s.engine.Use(chimw.Recoverer)
	s.engine.Use(chimw.RealIP)
	s.engine.Use(chimw.Timeout(120 * time.Second))
	if limit := inflightLimit(s.cfg.Server.MaxInflightRequests); limit > 0 {
		s.engine.Use(inflightGuard(limit))
	}
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

// noStore marks every response as uncacheable. Authenticated HTML
// (admin console, account pages) and per-key JSON must never be
// served from the browser's back/forward or HTTP cache: after
// logout, Back would otherwise render the stale logged-in snapshot
// without asking the server. Vary: Cookie keeps any shared cache
// from mixing sessions. Registered globally so no route can opt in
// to caching by omission.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		h.Set("Pragma", "no-cache")
		h.Add("Vary", "Cookie")
		next.ServeHTTP(w, r)
	})
}

// requestLogger is a lightweight replacement for chimw.Logger.
// It logs every request as "METHOD /path STATUS DURATION" to
// stdout without the Apache combined format overhead.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &loggingResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(lw, r)
		fmt.Fprintf(os.Stdout, "%s %s %d %s\n",
			r.Method, r.URL.Path, lw.status, time.Since(start))
	})
}

// loggingResponseWriter wraps http.ResponseWriter to capture the
// status code written by the next handler.
type loggingResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *loggingResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *loggingResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *loggingResponseWriter) CloseNotify() <-chan bool {
	if cn, ok := w.ResponseWriter.(http.CloseNotifier); ok {
		return cn.CloseNotify()
	}
	return nil
}

// inflightLimit resolves the configured max-inflight guard: unset
// (0) means the 10000 default; a negative value explicitly disables
// the guard.
func inflightLimit(cfgMax int) int {
	if cfgMax < 0 {
		return 0
	}
	if cfgMax == 0 {
		return 10000
	}
	return cfgMax
}

// inflightGuard rejects requests when the number of concurrently
// in-flight requests exceeds limit, returning 503 immediately. It
// protects the process from connection/fd exhaustion cascades (the
// observed accept4: too many open files failure mode): when any
// downstream component stalls, new work is shed instead of piling
// up connections. The guard counts every request after the header
// is read; streaming requests hold their slot until the stream
// completes.
func inflightGuard(limit int) func(http.Handler) http.Handler {
	var inflight int64
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if atomic.AddInt64(&inflight, 1) > int64(limit) {
				atomic.AddInt64(&inflight, -1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":{"message":"gateway overloaded","type":"api_error","code":"overloaded"}}`))
				return
			}
			defer atomic.AddInt64(&inflight, -1)
			next.ServeHTTP(w, r)
		})
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

	// P21: wire the per-channel plan-quota tracker into both sides —
	// the emitter observes every request, the engine consults it in
	// the L2.5 guard and the quota_aware strategy. Startup recovery
	// replays the widest configured window from the logstore so the
	// in-memory counters survive restarts (the logs are the source
	// of truth; the tracker holds no persistence of its own).
	usageTrk := usage.New()
	handler.SetUsageTracker(usageTrk)
	s.router.SetUsageTracker(usageTrk)
	s.router.SetQuotaGuardEnabledFn(rt.QuotaGuardEnabled)
	s.router.SetQuotaSoftThreshold(rt.QuotaSoftThreshold())
	usage.RecoverRecent(usageTrk, s.logStore, s.store)
	logging.Info("usage tracker wired (P21)",
		logging.F("quota_guard", rt.QuotaGuardEnabled()),
		logging.F("soft_threshold", rt.QuotaSoftThreshold()),
	)

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
	s.router.SetHealthChecker(proberCache)
	logging.Info("prober: wired into router (real-traffic observer + L2 health filter)")

	// Session affinity for mode:auto combos (default off). When
	// enabled, multi-turn conversations pin to one model/channel to
	// preserve upstream prompt caches.
	if s.cfg.Session.Enabled {
		sessStore := session.New(session.Config{
			TTL:        time.Duration(s.cfg.Session.TTLSec) * time.Second,
			MaxEntries: s.cfg.Session.MaxEntries,
		})
		sessStore.StartCleanup(time.Minute)
		handler.SetSessions(sessStore)
		s.sessions = sessStore
		logging.Info("session_affinity: enabled",
			logging.F("ttl_sec", s.cfg.Session.TTLSec),
			logging.F("max_entries", s.cfg.Session.MaxEntries),
		)
	}

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
	adminHandler.SetUsageTracker(usageTrk)
	// P22: the usage monitor is constructed by main before Start(); if
	// it is already attached, give the admin handler the same instance.
	if s.usageMon != nil {
		adminHandler.SetUsageMonitor(s.usageMon)
	}
	s.admin = adminHandler
	if responseCache != nil {
		adminHandler.SetCache(responseCache)
	}
	adminHandler.SetMCPClientManager(s.mcpClientMgr)
	adminHandler.SetGuardrailEngine(handler.GuardrailEngine())
	adminHandler.SetAutoStats(handler.AutoStats())
	adminHandler.SetSessions(handler.Sessions())
	adminHandler.SetModelCosts(handler.ModelCosts())
	s.guardrailEngine = handler.GuardrailEngine()
	adminHandler.SetReloadNotifier(s.fireNotify)

	// WithLimitsAndOptions (vs. WithLimits) is what actually
	// fires the BYOK hook when an unknown bearer arrives. If
	// s.byokHook is nil we fall back to the strict 403 path —
	// same effective behaviour as the old WithLimits call.
	s.engine.With(authmw.WithLimitsAndOptions(s.tokens.Lookup, handler.Limits(), s.byokHook)).
		Mount("/v1", handler.Routes())

	// Public self-service billing endpoints (no bearer token; the
	// /me/* routes authenticate via the llmrx_session cookie):
	// invite-code registration + top-up redemption.
	handler.SetTokenReloader(s.tokens.Reload)
	s.engine.Post("/api/v1/register", handler.Register)
	s.engine.Get("/api/v1/me", handler.Me)
	s.engine.Post("/api/v1/me/redeem", handler.Redeem)

	// MCP server endpoint: expose llmRx's own tools. Same token
	// chain as /v1 — the endpoint can spend the gateway's upstream
	// keys via channel_invoke, so it must not be unauthenticated.
	s.engine.With(authmw.WithLimitsAndOptions(s.tokens.Lookup, handler.Limits(), s.byokHook)).
		Post("/mcp/llmrx", func(w http.ResponseWriter, r *http.Request) {
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
		// version is additive: existing clients read status and
		// intent_backend only, so adding a field is backward compat.
		fmt.Fprintf(w, `{"status":"ok","intent_backend":%q,"version":%q}`,
			s.router.IntentBackend(), s.buildVersionOrDev())
	})

	// Phase 0: html/template admin UI. Legacy JSON API still
	// available under /admin/api/v1 for backwards compatibility.
	webAPIBridge := webui.NewWebAPIBridge(s.store)
	webAPIBridge.SetLogBroker(lb)
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
	s.webUI = webUI
	webUI.SetProber(proberCache)
	webUI.SetUsageTracker(usageTrk)
	// P22: probe cookies are write-only secrets; seal them with the same
	// AES-GCM key that protects channel API keys.
	if sp, ok := s.store.(store.SecretsProvider); ok {
		if mgr := sp.SecretsManager(); mgr != nil {
			webui.SetCookieSealer(func(raw string) (string, error) {
				return mgr.Encrypt([]byte(raw))
			})
		}
	}
	if responseCache != nil {
		webUI.SetCache(responseCache)
	}
	webUI.SetMCPClientManager(s.mcpClientMgr)
	webUI.SetOAuthManager(oauthMgr)
	if s.usageMon != nil {
		webUI.SetUsageMonitor(s.usageMon)
	}
	// P14: the row-level balance refresh button. Set here as well as
	// in SetBalanceMonitor because webUI is constructed after New
	// returns, so the setter may have run before this handler existed.
	if s.balanceMon != nil {
		webUI.SetBalanceMonitor(s.balanceMon)
	}
	s.engine.Mount("/account", webUI.UserRoutes())
	s.engine.Mount("/admin", webUI.Routes())
	s.engine.Mount("/admin/api/v1", adminHandler.Routes())

	// P15: edge-client usage ingest. Mounted only when edge clients are
	// configured — an empty list leaves no /client/v1 surface at all.
	if len(s.cfg.EdgeClients) > 0 {
		edgeClients := make([]clientapi.EdgeClient, 0, len(s.cfg.EdgeClients))
		for _, c := range s.cfg.EdgeClients {
			if strings.TrimSpace(c.Token) == "" {
				logging.Warn("edge client with empty token skipped",
					logging.F("name", c.Name))
				continue
			}
			edgeClients = append(edgeClients, clientapi.EdgeClient{Name: c.Name, Token: c.Token})
		}
		if len(edgeClients) > 0 {
			ing := clientapi.NewIngestor(s.logStore, edgeClients)
			// P15 phase 3: the persistent registry enables enrollment.
			// A deployment that only uses config.yml static tokens simply
			// never calls the enroll endpoint.
			ing.SetEnrollStore(s.store)
			s.engine.Mount("/", ing.Handler())
			logging.Info("edge client ingest enabled",
				logging.F("clients", len(edgeClients)))
			s.edgeIngestor = ing
			// P15 phase 5: the admin Clients page. Both the JSON API
			// (admin handler) and the HTML page (webui) get the ingestor
			// through narrow interfaces.
			s.admin.SetEdgeIngestor(ing)
			webUI.SetEdgeControl(ing)
		}
	}
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
		WriteTimeout:      10 * time.Minute, // generous upper bound; chi per-route timeouts handle tighter limits
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
		if s.sessions != nil {
			s.sessions.Stop()
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

// SetBalanceMonitor injects the upstream balance monitor (P14). The
// admin handler serves a read-only snapshot and a synchronous
// per-channel refresh through it.
func (s *Server) SetBalanceMonitor(m *balance.Monitor) {
	s.balanceMon = m
	if s.admin != nil {
		s.admin.SetBalanceMonitor(m)
	}
	// Same timing issue as SetUsageMonitor: main() builds the monitor
	// after New() returned, so the webui's copy made during route
	// registration saw nil. Without this the per-row balance refresh
	// button rendered by the channels page never worked.
	if s.webUI != nil {
		s.webUI.SetBalanceMonitor(m)
	}
}

// SetUsageMonitor installs the P22 vendor-side subscription usage
// monitor. Like the balance monitor it is built even when the sweep is
// disabled, because it backs the manual refresh endpoint.
func (s *Server) SetUsageMonitor(m *subusage.Monitor) {
	s.usageMon = m
	if s.admin != nil {
		s.admin.SetUsageMonitor(m)
	}
	// main() calls this after New() has already registered routes and
	// built the webui handler, so push it there too.
	if s.webUI != nil {
		s.webUI.SetUsageMonitor(m)
	}
	// P22: the router reads the vendor report from the channel's cached
	// state. The monitor is the natural carrier of that reading, so
	// hand it to the engine as the vendor-side quota source.
	if m != nil {
		s.router.SetVendorUsageTracker(m)
	}
}

// SetReloadNotifier installs the cross-replica notify sender
// (PG NOTIFY llmrx_reload). Called on local admin writes so other
// replicas reload promptly; nil (default) is a no-op.
func (s *Server) SetReloadNotifier(fn func()) { s.notifyFn = fn }

// SetClusterManager sets the cluster manager for multi-node mode.
// Must be called before Start() to register node API routes.
func (s *Server) SetClusterManager(mgr *cluster.Manager) {
	s.clusterManager = mgr
	// 重新注册节点路由
	if s.clusterManager != nil {
		nodeAPI := cluster.NewAPIHandler(s.clusterManager)
		s.engine.Group(func(r chi.Router) {
			r.Use(cluster.InternalOnlyMiddleware)
			r.Mount("/nodes", nodeAPI.Routes())
		})
	}
}

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
