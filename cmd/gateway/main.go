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

	cp := pool.NewChannelPool(cfg)
	eng := router.New(cfg, cp)
	srv := server.New(cfg, eng, cp)

	log.Printf("starting llmRx gateway on :%d", cfg.Server.Port)
	if err := srv.Start(); err != nil {
		log.Fatalf("server: %v", err)
	}
}
