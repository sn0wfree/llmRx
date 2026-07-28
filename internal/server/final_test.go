package server

import (
	"context"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestStart_EmptyHost(t *testing.T) {
	app := testhelper.New(t)
	cfg := &config.Config{Server: config.ServerConfig{Host: "", Port: 0}}
	s := New(cfg, "", app.Engine, app.Pool, app.Store, app.LogStore, app.Cache, app.LogBroker, app.RT, "")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	_ = s.Start(ctx)
}

func TestStart_WithTokens(t *testing.T) {
	app := testhelper.New(t)
	cfg := &config.Config{Server: config.ServerConfig{Host: "127.0.0.1", Port: 0}}
	s := New(cfg, "", app.Engine, app.Pool, app.Store, app.LogStore, app.Cache, app.LogBroker, app.RT, "")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	_ = s.Start(ctx)
}
