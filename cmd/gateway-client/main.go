package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sn0wfree/llmRx/internal/client"
)

// gateway-client — the llmRx edge client (P15).
//
// Serves an OpenAI-compatible endpoint on localhost and forwards to a
// remote llmRx server, with a local response cache, an offline
// fallback chain and usage accounting. One binary, no database, no
// CGO: see docs/P15-CLIENT.md.
//
// Client config (client.yml):
//
//	listen: 127.0.0.1:3425
//	servers:
//	  - https://llmrx.example.com
//	token: sk-llmrx-user-token      # injected when the agent sends none
//	client_name: laptop-1
//	client_token: sk-edge-secret    # enables usage upload
//	cache_enabled: true
//	usage_file: ~/.llmrx/usage.jsonl
func main() {
	cfgPath := flag.String("config", "client.yml", "client config file path")
	showVersion := flag.Bool("version", false, "print the build version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildInfoString())
		return
	}

	cfg, err := loadClientConfig(*cfgPath)
	if err != nil {
		fatal(err)
	}
	if err := cfg.Normalize(); err != nil {
		fatal(err)
	}

	proxy := client.NewProxy(cfg)
	var usageStore client.UsagePersister
	if cfg.UsageFile != "" {
		usageStore, err = client.NewJSONLUsageStore(cfg.UsageFile)
		if err != nil {
			fatal(err)
		}
		defer func() { _ = usageStore.Close() }()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxy.SetUsageSink(usageStore)
	go proxy.StartUsageRecorder(ctx, usageStore)
	go proxy.StartUsageUploader(ctx, &http.Client{Timeout: 10 * time.Second})

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           proxy.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Bind BEFORE printing ready: a supervisor's readiness check
	// between the log line and the listen would race.
	if err := listenAndServeBackground(srv); err != nil {
		fatal(err)
	}
	fmt.Printf("llmRx client listening on %s → %s (version %s)\n",
		cfg.Listen, cfg.Servers[0], buildInfoString())

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	fmt.Println("shutting down: draining in-flight requests…")
	shutdownCtx, sc := context.WithTimeout(context.Background(), 15*time.Second)
	defer sc()
	_ = srv.Shutdown(shutdownCtx)
	_ = proxy.Close()
	fmt.Println("bye")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gateway-client:", err)
	os.Exit(1)
}

// listenAndServeBackground starts the HTTP server without blocking and
// returns once the socket is bound. Binding before announcing readiness
// avoids the race where a supervisor's health check runs between the
// ready log line and the actual listen.
func listenAndServeBackground(srv *http.Server) error {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", srv.Addr, err)
	}
	go func() {
		// Serve always returns a non-nil error; ErrServerClosed is the
		// expected result of a clean Shutdown.
		if serr := srv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "gateway-client: serve:", serr)
			os.Exit(1)
		}
	}()
	return nil
}

func loadClientConfig(path string) (*client.Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w (start from the sample in docs/P15-CLIENT.md)", path, err)
	}
	var cfg client.Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}
