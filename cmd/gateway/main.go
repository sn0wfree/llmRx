package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"time"

	"github.com/sn0wfree/llmRx/internal/alert"
	"github.com/sn0wfree/llmRx/internal/alert/channels"
	"github.com/sn0wfree/llmRx/internal/auth"
	"github.com/sn0wfree/llmRx/internal/broker"
	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/runtime"
	"github.com/sn0wfree/llmRx/internal/secrets"
	"github.com/sn0wfree/llmRx/internal/server"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/tokencache"
)

func main() {
	cfgPath := flag.String("config", "config.yml", "config file path")
	hcAddr := flag.String("healthcheck", "", "if set (e.g. 127.0.0.1:8787), probe /health and exit; used by docker HEALTHCHECK")
	flag.Parse()

	// `-healthcheck addr` short-circuits before any side-effects: no
	// config load, no DB open, no privilege drop. The probe just
	// dials addr, sends GET /health, and returns 0 on HTTP 200.
	if *hcAddr != "" {
		os.Exit(runHealthcheck(*hcAddr, 5*time.Second))
	}

	// Resolve LLMRX_KEY_MASTER (env → /data/llmrx.key → generate).
	// Must run BEFORE privilege drop and BEFORE secrets.FromEnv.
	if err := bootstrapMasterKey("LLMRX_KEY_MASTER", "/data/llmrx.key"); err != nil {
		log.Fatalf("secrets: %v", err)
	}

	// If running as root (typical docker entrypoint), chown bind-
	// mounted /data and drop to llmrx before opening the DB.
	if err := maybeChownDataDir("/data", "llmrx"); err != nil {
		log.Printf("secrets: chown /data: %v (continuing — DB writes may fail)", err)
	}
	// Write a starter config.yml if /data is fresh — lets `docker
	// compose up` Just Work without a manual config step.
	if err := maybeWriteStarterConfig("/data", "/data/config.yml"); err != nil {
		log.Printf("config: %v (continuing — provide your own config.yml)", err)
	}
	if err := dropPrivileges("llmrx"); err != nil {
		log.Fatalf("secrets: %v", err)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	st, err := store.OpenSQLite(cfg.Database.DSN)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// Attach a secrets manager for at-rest encryption of channel
	// API keys. Required in production; dev-only plaintext mode
	// is gated by DEV_ALLOW_PLAINTEXT_KEYS.
	if !cfg.Secrets.DevAllowPlaintext {
		sec, err := secrets.FromEnv(cfg.Secrets.KeyMasterEnv)
		if err != nil {
			log.Fatalf("secrets: %v", err)
		}
		st.SetSecrets(sec)
		log.Printf("secrets: master key loaded from %s (AES-256-GCM)", sec.EnvName())
	} else {
		log.Printf("secrets: DEV_ALLOW_PLAINTEXT_KEYS=true — channel API keys will be stored in plaintext. Do NOT use this in production.")
	}

	if err := seed(st, cfg); err != nil {
		log.Fatalf("seed: %v", err)
	}

	cp := pool.NewChannelPool()
	if err := cp.LoadFromStore(st); err != nil {
		log.Fatalf("load pool: %v", err)
	}

	tokCache := tokencache.New(st)
	eng := router.New(st, cp)
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
		log.Printf("runtime: read settings: %v (continuing with YAML seeds)", err)
	} else if len(raw) > 0 {
		var snap runtime.Snapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			log.Printf("runtime: parse settings: %v (continuing with YAML seeds)", err)
		} else {
			rt.Apply(snap)
			if snap.CostStrategy != "" {
				eng.SetStrategy(model.CostStrategy(snap.CostStrategy))
			}
			log.Printf("runtime: applied persisted settings on top of YAML seeds")
		}
	}
	// 3) Propagate values to subsystems that need them at startup:
	//    the SSE broker must use the final cap (YAML or DB), and
	//    the standard log package must be routed through the
	//    level filter so admin log_level changes take effect.
	logBroker.SetMaxSubscribers(rt.MaxLogSubscribers())
	runtime.InstallLogFilter(rt, os.Stderr)
	log.Printf("runtime: log level = %s", runtime.LogLevelName(rt.LogLevel()))

	alertMgr := alert.NewManager(st, []alert.Channel{
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
	go logRetentionLoop(ctx, st, rt)
	go alertMgr.Start(ctx)

	srv := server.New(cfg, eng, cp, st, tokCache, logBroker, rt, "/data/llmrx.key")
	srv.SetAlertManager(alertMgr)

	log.Printf("starting llmRx gateway on :%d (channels=%d tokens=%d db=%s)",
		cfg.Server.Port, len(cp.GetAllChannels()), tokCache.Size(), cfg.Database.DSN)
	if err := srv.Start(); err != nil {
		log.Fatalf("server: %v", err)
	}
}

// cleanupLoop periodically clears admin session tokens whose
// session_exp is in the past. Runs every 5 minutes; exits when ctx
// is cancelled or the process exits.
func cleanupLoop(ctx context.Context, st store.Store) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := st.CleanupExpiredSessions(); err != nil {
				log.Printf("cleanup sessions: %v", err)
			} else if n > 0 {
				log.Printf("cleanup: cleared %d expired admin sessions", n)
			}
		}
	}
}

// logRetentionLoop deletes log rows older than retentionDays once a
// day. retentionDays <= 0 disables the loop. Reads the current
// retention window from rt on every tick so admin updates take
// effect without a restart; runs once immediately on startup so
// admin changes don't have to wait 24h to see effect.
func logRetentionLoop(ctx context.Context, st store.Store, rt *runtime.Defaults) {
	if rt.LogRetentionDays() <= 0 {
		return
	}
	sweep := func() {
		retentionDays := int(rt.LogRetentionDays())
		if retentionDays <= 0 {
			return
		}
		cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).Unix()
		if n, err := st.DeleteLogsBefore(cutoff); err != nil {
			log.Printf("log retention: %v", err)
		} else if n > 0 {
			log.Printf("log retention: deleted %d rows older than %d days", n, retentionDays)
		}
	}
	sweep() // B3: run on startup so admin changes don't need 24h wait
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
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

func seedAdmin(st store.Store, cfg *config.Config) error {
	if u, _ := st.GetUserByUsername("admin"); u != nil {
		return nil
	}
	pw := cfg.Server.AdminPassword
	if pw == "" {
		pw = "admin"
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
	log.Printf("seed: created default admin user (username=admin password=%s)", pw)
	return nil
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
		log.Printf("seed: imported %d tokens from cfg", len(cfg.Tokens))
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
			masked := maskKey(k)
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
		log.Printf("seed: imported channel %s with %d keys", cc.Name, len(cc.Keys))
	}
	return nil
}

func maskKey(k string) string {
	if len(k) > 8 {
		return k[:4] + "***" + k[len(k)-4:]
	}
	return k
}