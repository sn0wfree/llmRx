package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	_ "net/http/pprof" // registers pprof handlers on the default mux
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sn0wfree/llmRx/internal/alert"
	"github.com/sn0wfree/llmRx/internal/alert/channels"
	"github.com/sn0wfree/llmRx/internal/auth"
	"github.com/sn0wfree/llmRx/internal/broker"
	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/dialect"
	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/logstore"
	authmw "github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/modelmeta"
	"github.com/sn0wfree/llmRx/internal/notify"
	"github.com/sn0wfree/llmRx/internal/observability"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/ratelimit"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/runtime"
	"github.com/sn0wfree/llmRx/internal/secrets"
	"github.com/sn0wfree/llmRx/internal/server"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/tokencache"

	"gopkg.in/yaml.v3"
)

// fatalf emits a structured error log and exits with code 1.
// It replaces log.Fatalf so the JSON logger still receives the
// event before the process dies.
func fatalf(msg string, fields ...logging.Field) {
	logging.Error(msg, fields...)
	os.Exit(1)
}

func main() {
	cfgPath := flag.String("config", "config.yml", "config file path")
	hcAddr := flag.String("healthcheck", "", "if set (e.g. 127.0.0.1:8787), probe /health and exit; used by docker HEALTHCHECK")
	wipeKeys := flag.Bool("wipe-keys", false, "clear all encrypted key material in the keys table, then exit. Used to recover from a master-key rotation that left stored ciphertext undecryptable.")
	pprofAddr := flag.String("pprof-addr", "", "if set (e.g. 127.0.0.1:6060), serve net/http/pprof profiles on a separate listener; intended for on-demand performance debugging, never expose publicly")
	flag.Parse()

	if *pprofAddr != "" {
		go servePprof(*pprofAddr)
	}

	// `-healthcheck addr` short-circuits before any side-effects: no
	// config load, no DB open, no privilege drop. The probe just
	// dials addr, sends GET /health, and returns 0 on HTTP 200.
	if *hcAddr != "" {
		os.Exit(runHealthcheck(*hcAddr, 5*time.Second))
	}

	// `-wipe-keys` short-circuits BEFORE bootstrapMasterKey and
	// privilege drop: it's a single SQL UPDATE that doesn't read
	// any ciphertext, so it must work even when the master key
	// has rotated and the rest of the gateway can't decrypt
	// anything. Config + DB are still required (we need the DSN
	// and an open handle to run the UPDATE).
	if *wipeKeys {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			fatalf("wipe-keys load config failed", logging.F("error", err.Error()))
		}
		st, err := store.Open(cfg.Database.Driver, cfg.Database.DSN)
		if err != nil {
			fatalf("wipe-keys open store failed", logging.F("error", err.Error()))
		}
		n, werr := st.WipeKeys()
		_ = st.Close()
		if werr != nil {
			fatalf("wipe-keys failed", logging.F("error", werr.Error()))
		}
		logging.Info("wipe-keys cleared key rows", logging.F("count", n))
		return
	}

	// If running as root (typical docker entrypoint), chown
	// bind-mounted /data before opening the DB.
	if err := maybeChownDataDir("/data", "llmrx"); err != nil {
		logging.Warn("secrets chown /data failed",
			logging.F("error", err.Error()),
		)
	}
	// Write a starter config.yml if /data is fresh so that
	// `docker compose up` works without a manual config
	// step. Must run BEFORE config.Load.
	if err := maybeWriteStarterConfig("/data", *cfgPath); err != nil {
		logging.Warn("config starter write failed",
			logging.F("error", err.Error()),
		)
	}

	// Load config so we can honour dev_allow_plaintext_keys
	// when deciding whether to require the master key. In
	// production, set LLMRX_KEY_MASTER via the orchestrator;
	// dev mode skips encryption via cfg.Secrets.DevAllowPlaintext.
	// If neither is set, a key is auto-generated and persisted
	// to /data/llmrx.key so docker compose Just Works.
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatalf("load config failed", logging.F("error", err.Error()))
	}

	// Resolve LLMRX_KEY_MASTER (env → /data/llmrx.key). Must run
	// BEFORE privilege drop and BEFORE secrets.FromEnv. No-op
	// when dev_allow_plaintext_keys is set.
	if err := bootstrapMasterKey("LLMRX_KEY_MASTER", "/data/llmrx.key", cfg.Secrets.DevAllowPlaintext); err != nil {
		fatalf("secrets failed", logging.F("error", err.Error()))
	}

	if err := dropPrivileges("llmrx"); err != nil {
		fatalf("secrets failed", logging.F("error", err.Error()))
	}

	st, err := store.Open(cfg.Database.Driver, cfg.Database.DSN)
	if err != nil {
		fatalf("open store failed", logging.F("error", err.Error()))
	}
	defer st.Close()

	// Attach a secrets manager for at-rest encryption of channel
	// API keys. Required in production; dev-only plaintext mode
	// is gated by DEV_ALLOW_PLAINTEXT_KEYS.
	if !cfg.Secrets.DevAllowPlaintext {
		sec, err := secrets.FromEnv(cfg.Secrets.KeyMasterEnv)
		if err != nil {
			fatalf("secrets failed", logging.F("error", err.Error()))
		}
		st.SetSecrets(sec)
		logging.Info("secrets loaded master key", logging.F("env", sec.EnvName()))
	} else {
		logging.Warn("DEV_ALLOW_PLAINTEXT_KEYS enabled, keys stored plaintext")
	}

	// Initialize log store. Default: per-date SQLite files under
	// LogDir. Cluster mode (database.driver=postgres) uses the
	// shared Postgres logs table unless logstore_backend=sqlite is
	// set explicitly.
	logDir := cfg.Server.LogDir
	if logDir == "" {
		logDir = "data/logs"
	}
	logBackend := cfg.Server.LogstoreBackend
	if logBackend == "" {
		if cfg.Database.Driver == "postgres" {
			logBackend = "postgres"
		} else {
			logBackend = "sqlite"
		}
	}
	var logDriver logstore.Driver
	switch logBackend {
	case "postgres":
		d, err := logstore.NewPostgresDriver(cfg.Database.DSN)
		if err != nil {
			fatalf("logstore failed", logging.F("error", err.Error()))
		}
		logDriver = d
		logging.Info("logstore backend: postgres (shared logs table)")
	case "sqlite":
		if err := logstore.EnsureDir(logDir); err != nil {
			fatalf("logstore failed", logging.F("error", err.Error()))
		}
		drv := logstore.NewSQLiteDriver()
		if err := drv.SetSynchronous(cfg.Server.LogstoreSynchronous); err != nil {
			fatalf("logstore failed", logging.F("error", err.Error()))
		}
		logDriver = drv
		if cfg.Server.LogstoreSynchronous == "off" {
			logging.Warn("logstore synchronous=off — crash may lose the most recent ~1s of logs")
		}
		logging.Info("logstore backend: sqlite", logging.F("path", logDir))
	default:
		fatalf("logstore backend", logging.F("backend", logBackend))
	}
	logStore, err := logstore.New(logDir, logDriver)
	if err != nil {
		fatalf("logstore failed", logging.F("error", err.Error()))
	}
	defer logStore.Close()

	// Load provider descriptors from config.yml and DB into the
	// provider registry. Built-in providers from providers.yaml are
	// already loaded via init(); this merges operator overrides and
	// user-defined providers from the DB.
	if len(cfg.Providers) > 0 {
		yamlBytes, _ := yaml.Marshal(map[string]any{"providers": cfg.Providers})
		if err := provider.LoadProvidersFromYAML(yamlBytes, "config"); err != nil {
			logging.Warn("provider load failed", logging.F("error", err.Error()))
		}
	}
	if defs, err := st.GetProviderDefs(); err == nil {
		for _, d := range defs {
			provider.RegisterProvider(provider.ProviderDesc{
				Name:           d.Name,
				DisplayName:    d.DisplayName,
				Protocol:       d.Protocol,
				DefaultBaseURL: d.BaseURL,
				Source:         "db",
			})
		}
	}

	// Initialize model metadata registry. Loads from
	// /data/models.json if present (operator-managed,
	// can be refreshed via scripts/fetch-models.sh),
	// otherwise falls back to the built-in catalog
	// compiled into the binary. A background goroutine
	// re-reads the file every 24h so updates take
	// effect without a restart.
	modelsPath := "/data/models.json"
	if err := modelmeta.Init(modelsPath); err != nil {
		logging.Warn("modelmeta init failed", logging.F("error", err.Error()))
	} else {
		logging.Info("modelmeta loaded", logging.F("models", modelmeta.Count()))
	}
	go modelmeta.StartRefreshLoop(24*time.Hour, modelsPath)

	if err := seed(st, cfg); err != nil {
		fatalf("seed failed", logging.F("error", err.Error()))
	}

	cp := pool.NewChannelPool()
	if err := cp.LoadFromStore(st); err != nil {
		logging.Info("hint cipher decrypt failed, run wipe-keys to recover")
		fatalf("load pool failed", logging.F("error", err.Error()))
	}

	tokCache := tokencache.New(st)
	// Opportunistically flip expired tokens to TokenExpired so
	// later cache reloads skip them. Best-effort: a failure is
	// logged but does not abort startup (the row stays TokenActive
	// and the expiry check in middleware still rejects it).
	tokCache.SetExpirer(func(tokenID int64) error {
		return st.MarkTokenExpired(tokenID)
	})
	// Re-run Reload now that the expirer is wired, and surface
	// any plan-join failure (fail-closed: a transient DB blip
	// must NOT silently downgrade bound tokens to "unlimited").
	if err := tokCache.Reload(); err != nil {
		fatalf("token cache reload failed", logging.F("error", err.Error()))
	}
	eng := router.New(st, cp)

	// Hydrate L5 (Thompson sampling) posteriors from the persisted
	// state file. Missing file is a no-op (first run); malformed
	// file is a hard error so we don't silently fall back to the
	// uniform prior and undo weeks of learned weights.
	thompsonPath := "/data/thompson.json"
	if err := eng.LoadThompsonState(thompsonPath); err != nil {
		fatalf("thompson load state failed", logging.F("path", thompsonPath), logging.F("error", err.Error()))
	} else {
		logging.Info("thompson state loaded", logging.F("path", thompsonPath))
	}

	// L4 Intent classifier. See loadIntentClassifier for the
	// LLMRX_INTENT_REQUIRED fail-closed semantics.
	if classifier, backend, err := loadIntentClassifier(); err != nil {
		fatalf("fatal error", logging.F("error", err.Error()))
	} else {
		eng.SetIntentClassifier(classifier)
		logging.Info("intent backend", logging.F("backend", backend))
	}
	logBroker := broker.New[*model.Log](cfg.Server.MaxLogSubscribers)
	defer logBroker.Close()

	rt := runtime.New()
	// 1) YAML seeds: the user-supplied config file is the
	//    "factory default" for every tunable.
	rt.SetMarkupRatio(cfg.Server.MarkupRatio)
	rt.SetBreakerMaxFailures(int64(cfg.Server.BreakerMax))
	rt.SetBreakerResetTimeoutMs(int64(cfg.Server.BreakerResetMs))
	rt.SetAlertCooldownSec(int64(cfg.Server.AlertCooldownSec))
	rt.SetLogRetentionDays(int64(cfg.Server.LogRetentionDays))
	rt.SetStreamTimeoutSec(int64(cfg.Server.StreamTimeoutSec))
	rt.SetStreamMaxBodyBytes(int64(cfg.Server.StreamMaxBodyBytes))
	rt.SetMaxLogSubscribers(int64(cfg.Server.MaxLogSubscribers))
	if s := cfg.Strategy.CostStrategy; s != "" {
		// B4: cost strategy must seed BOTH the router engine and
		// the runtime snapshot so the API/UI return the same
		// value the router is actually using.
		rt.SetCostStrategy(s)
		eng.SetStrategy(model.CostStrategy(s))
	}
	// 2) DB override: any admin changes persisted to
	//    runtime_settings take precedence over the YAML seeds.
	if raw, err := st.GetRuntimeSettings(); err != nil {
		logging.Warn("runtime read settings failed", logging.F("error", err.Error()))
	} else if len(raw) > 0 {
		var snap runtime.Snapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			logging.Warn("runtime parse settings failed", logging.F("error", err.Error()))
		} else {
			rt.Apply(snap)
			if snap.CostStrategy != "" {
				eng.SetStrategy(model.CostStrategy(snap.CostStrategy))
			}
			logging.Info("runtime applied persisted settings on top of YAML seeds")
		}
	}
	// 3) Propagate values to subsystems that need them at startup:
	//    the SSE broker must use the final cap (YAML or DB), and
	//    the standard log package must be routed through the
	//    level filter so admin log_level changes take effect.
	logBroker.SetMaxSubscribers(rt.MaxLogSubscribers())
	runtime.InstallLogFilter(rt, os.Stderr)
	logging.Info("runtime log level", logging.F("level", runtime.LogLevelName(rt.LogLevel())))

	// Wire rt into the circuit breaker so admin /runtime config
	// writes (breaker_max_failures, breaker_reset_timeout_ms)
	// take effect on the next request, not on restart.
	eng.SetBreakerDefaults(rt)

	alertMgr := alert.NewManager(st, logStore, []alert.Channel{
		channels.NewBuiltin(),
		channels.NewWebhook(),
	}, alert.Config{
		// B2: DefaultCooldown is the fallback when defaults is
		// nil. With Defaults set, the manager reads the live
		// AlertCooldownSec() on every evaluation so admin updates
		// take effect without a restart.
		DefaultCooldown: time.Duration(cfg.Server.AlertCooldownSec) * time.Second,
		Defaults:        rt,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cleanupLoop(ctx, st)
	// Pass a provider function so admin updates to
	// log_retention_days take effect on the next sweep instead
	// of being frozen at goroutine start.
	go logStore.RunRetention(ctx, func() int { return int(rt.LogRetentionDays()) })
	go logStore.RunCheckpoints(ctx)
	go alertMgr.Start(ctx)

	// BYOK (bring-your-own-key) is not implemented: the previous
	// wiring probed unknown sk- bearers with a no-op stub, silently
	// "registered" them, and then returned an empty 200 without ever
	// forwarding the request. Rather than keep that fail-open stub,
	// refuse to start when the feature is enabled so the operator
	// gets an explicit error instead of a silently broken feature.
	var byokHook authmw.UnknownTokenHook
	if cfg.BYOK.Enabled {
		fatalf("byok.enabled is set but BYOK is not implemented — set byok.enabled: false and create channels with the admin UI instead")
	}

	// P12 M2: cluster mode. With a Postgres store, the rate limiter
	// shares minute-bucket counters across replicas (PGWindowBackend)
	// and config changes propagate via LISTEN/NOTIFY with a 30s poll
	// fallback. Single-node SQLite keeps the process-local limiter
	// and no propagation goroutines.
	var lim *ratelimit.Limiter
	if cfg.Database.Driver == "postgres" {
		pgBackend, err := ratelimit.NewPGWindowBackend(st.RawDB(), dialect.Postgres{})
		if err != nil {
			fatalf("ratelimit backend failed", logging.F("error", err.Error()))
		}
		defer pgBackend.Close()
		lim = ratelimit.NewWithBackend(pgBackend)
		logging.Info("ratelimit: cluster backend (postgres minute buckets)")
	}

	srv := server.New(cfg, *cfgPath, eng, cp, st, logStore, tokCache, logBroker, rt, "/data/llmrx.key", byokHook, lim)
	srv.SetAlertManager(alertMgr)

	// Cross-replica config propagation (P12 M2): NOTIFY listener +
	// 30s polling fallback. Only in cluster mode.
	if cfg.Database.Driver == "postgres" {
		srv.SetReloadNotifier(func() {
			_, _ = st.RawDB().Exec("SELECT pg_notify('llmrx_reload', '')")
		})
		go notify.Listen(ctx, cfg.Database.DSN, srv.ReloadConfig)
		go notify.Poll(ctx, 30*time.Second, srv.ReloadConfig)
		logging.Info("config propagation: notify listener + 30s poll active")
	}

	// Hook SIGINT/SIGTERM into ctx so server.Start drains in-flight
	// requests instead of hard-cutting active chat completions
	// mid-stream. The kubelet / docker stop sequence uses SIGTERM
	// with a default 30s grace period; srv.Start matches that with
	// a 25s shutdown timeout (see server.Start).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logging.Info("shutdown signal received", logging.F("signal", sig.String()))
		// Persist L5 posteriors BEFORE the listener shuts down so
		// the next process boots with the learned channel weights
		// intact. Best-effort: a write error here shouldn't block
		// the shutdown.
		if err := eng.SaveThompsonState(thompsonPath); err != nil {
			logging.Warn("thompson save state failed", logging.F("error", err.Error()))
		} else {
			logging.Info("thompson state saved", logging.F("path", thompsonPath))
		}
		cancel()
	}()

	// Periodic snapshot so a crash (no SIGTERM) doesn't lose hours
	// of L5 learning. The file is small and written atomically via
	// a temp file, so a 5-minute cadence is cheap.
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := eng.SaveThompsonState(thompsonPath); err != nil {
					logging.Warn("thompson periodic save failed", logging.F("error", err.Error()))
				}
			}
		}
	}()

	logging.Info("starting llmRx gateway",
		logging.F("port", cfg.Server.Port),
		logging.F("channels", len(cp.GetAllChannels())),
		logging.F("tokens", tokCache.Size()),
		logging.F("db", cfg.Database.DSN),
	)

	// Initialize structured JSON logger + Prometheus metrics.
	logging.Init(logging.LevelInfo, logging.FormatJSON)
	observability.Init()
	stopMetrics := srv.StartMetricsServer(ctx)
	defer stopMetrics()

	// Set initial gauge values.
	observability.SetChannelsEnabled(float64(len(cp.GetAllChannels())))
	observability.SetTokensActive(float64(tokCache.Size()))

	if err := srv.Start(ctx); err != nil {
		fatalf("server failed", logging.F("error", err.Error()))
	}
}

// servePprof exposes net/http/pprof (cpu/heap/goroutine/allocs
// profiles) on a dedicated listener when -pprof-addr is set. The
// default mux is used because the pprof package registers its
// handlers there on import. Bind to 127.0.0.1 or an internal
// network address — never expose publicly.
func servePprof(addr string) {
	err := http.ListenAndServe(addr, nil)
	if err != nil {
		logging.Warn("pprof listener failed", logging.F("addr", addr), logging.F("error", err.Error()))
	}
}

// cleanupLoop periodically clears admin session tokens whose
// session_exp is in the past. Runs every 5 minutes; exits when ctx
// is cancelled or the process exits.
func cleanupLoop(ctx context.Context, st store.Store) {	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := st.CleanupExpiredSessions(); err != nil {
				logging.Warn("cleanup sessions failed", logging.F("error", err.Error()))
			} else if n > 0 {
				logging.Info("cleanup cleared expired admin sessions", logging.F("count", n))
			}
		}
	}
}

// seed populates the database with admin user, plans, tokens,
// channels and keys from cfg when those tables are empty.
func seed(st store.Store, cfg *config.Config) error {
	if err := seedAdmin(st, cfg); err != nil {
		return err
	}
	if err := seedTokens(st, cfg); err != nil {
		return err
	}
	return seedChannels(st, cfg)
}

// seedAdmin bootstraps the root user on a fresh database.
//
// Security: refusing to create the well-known admin/admin
// credential unless the operator explicitly opts in
// (cfg.Server.AllowDefaultAdminPassword == true). Without this
// gate, every fresh install ships with a publicly known root
// password. The default "admin" password is logged at info
// level (length + first/last char) so the operator can still
// verify what was seeded, but the plaintext itself never
// appears in the log.
func seedAdmin(st store.Store, cfg *config.Config) error {
	if u, _ := st.GetUserByUsername("admin"); u != nil {
		return nil
	}
	pw := cfg.Server.AdminPassword
	if pw == "" {
		pw = "admin"
		if !cfg.Server.AllowDefaultAdminPassword {
			return fmt.Errorf("refusing to seed default admin/admin — set server.admin_password in config or set server.allow_default_admin_password: true (NOT for production)")
		}
	}
	hashed, err := auth.Hash(pw)
	if err != nil {
		return err
	}
	u := &model.User{
		Username:     "admin",
		PasswordHash: hashed,
		Role:         model.RoleRoot,
		Status:       1,
	}
	if err := st.CreateUser(u); err != nil {
		return err
	}
	logging.Info("seed created default admin user",
		logging.F("username", "admin"),
		logging.F("password_len", len(pw)),
		logging.F("first", firstRune(pw)),
		logging.F("last", lastRune(pw)),
	)
	return nil
}

func firstRune(s string) string {
	if s == "" {
		return ""
	}
	for _, r := range s {
		return string(r)
	}
	return ""
}

func lastRune(s string) string {
	if s == "" {
		return ""
	}
	runes := []rune(s)
	return string(runes[len(runes)-1])
}

func seedTokens(st store.Store, cfg *config.Config) error {
	existing, err := st.GetTokens()
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	for _, t := range cfg.Tokens {
		nt := &model.Token{
			Key:             t.Key,
			Name:            t.Name,
			Status:          model.TokenActive,
			ModelsWhitelist: t.Models,
		}
		if err := st.CreateToken(nt); err != nil {
			return err
		}
	}
	if len(cfg.Tokens) > 0 {
		logging.Info("seed imported tokens from cfg", logging.F("count", len(cfg.Tokens)))
	}
	return nil
}

func seedChannels(st store.Store, cfg *config.Config) error {
	existing, err := st.GetChannels()
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	for _, cc := range cfg.Channels {
		proto := cc.Protocol
		if proto == "" {
			proto = "openai"
		}
		ch := &model.Channel{
			Name:        cc.Name,
			Provider:    cc.Provider,
			Protocol:    proto,
			BaseURL:     cc.BaseURL,
			Models:      cc.Models,
			Priority:    cc.Priority,
			InputPrice:  cc.InputPrice,
			OutputPrice: cc.OutputPrice,
			Status:      model.ChannelEnabled,
			CircuitBreaker: model.CircuitBreakerConfig{
				MaxFailures:  cc.MaxFailures,
				ResetTimeout: time.Duration(cc.ResetTimeoutMs) * time.Millisecond,
			},
		}
		if err := st.CreateChannel(ch); err != nil {
			return err
		}
		for _, k := range cc.Keys {
			masked := secrets.Mask(k)
			ke := &model.Key{
				ChannelID: ch.ID,
				Key:       k,
				KeyMasked: masked,
				Status:    model.KeyActive,
			}
			if err := st.CreateKey(ke); err != nil {
				return err
			}
		}
		logging.Info("seed imported channel", logging.F("channel", cc.Name), logging.F("keys", len(cc.Keys)))
	}
	return nil
}
