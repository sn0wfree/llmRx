package main

import (
	"flag"
	"log"

	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/pool"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/server"
)

func main() {
	cfgPath := flag.String("config", "config.yml", "config file path")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	validTokens := make(map[string]string, len(cfg.Tokens))
	for _, t := range cfg.Tokens {
		if t.Key == "" {
			continue
		}
		validTokens[t.Key] = t.Name
	}

	cp := pool.NewChannelPool(cfg)
	eng := router.New(cfg, cp)
	srv := server.New(cfg, eng, cp, validTokens)

	log.Printf("starting llmRx gateway on :%d (channels=%d tokens=%d)",
		cfg.Server.Port, len(cfg.Channels), len(validTokens))
	if err := srv.Start(); err != nil {
		log.Fatalf("server: %v", err)
	}
}