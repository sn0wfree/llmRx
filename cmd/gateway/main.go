package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"log"
	"time"

	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/server"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/tokencache"
)

func main() {
	cfgPath := flag.String("config", "config.yml", "config file path")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	st, err := store.OpenSQLite(cfg.Database.DSN)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	if err := seed(st, cfg); err != nil {
		log.Fatalf("seed: %v", err)
	}

	cp := pool.NewChannelPool()
	if err := cp.LoadFromStore(st); err != nil {
		log.Fatalf("load pool: %v", err)
	}

	tokCache := tokencache.New(st)
	eng := router.New(st, cp)
	srv := server.New(cfg, eng, cp, st, tokCache)

	log.Printf("starting llmRx gateway on :%d (channels=%d tokens=%d db=%s)",
		cfg.Server.Port, len(cp.GetAllChannels()), tokCache.Size(), cfg.Database.DSN)
	if err := srv.Start(); err != nil {
		log.Fatalf("server: %v", err)
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
	u := &model.User{
		Username:     "admin",
		PasswordHash: hashPassword(pw),
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
			Key:    t.Key,
			Name:   t.Name,
			Status: model.TokenActive,
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
		ch := &model.Channel{
			Name:        cc.Name,
			Provider:    cc.Provider,
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

// hashPassword is a minimal deterministic hash for the seed path
// only; real authentication is handled by the admin handler.
func hashPassword(pw string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	salt := hex.EncodeToString(b)
	return salt + ":" + pw
}

func verifyPassword(hash, pw string) bool {
	for i := 0; i+1 < len(hash); i++ {
		if hash[i] == ':' {
			return hash[i+1:] == pw
		}
	}
	return hash == pw
}

func newSessionToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}