// Package notify implements cross-replica configuration propagation
// over Postgres LISTEN/NOTIFY (P12 M2). A dedicated pgx connection
// subscribes to the llmrx_reload channel; every notification calls
// the reload callback (server.ReloadConfig). A 30s polling fallback
// is driven by the caller so a missed notification still converges
// (NOTIFY is best-effort — a dropped connection loses messages).
package notify

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sn0wfree/llmRx/internal/logging"
)

// ReloadChannel is the NOTIFY channel name for config reloads.
const ReloadChannel = "llmrx_reload"

// Listen subscribes to the reload channel and invokes onReload for
// every notification. It runs until ctx is cancelled, reconnecting
// with backoff on errors. Intended to run in its own goroutine.
func Listen(ctx context.Context, dsn string, onReload func()) {
	backoff := 5 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			logging.Warn("notify: connect failed", logging.F("error", err.Error()))
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			continue
		}
		if _, err := conn.Exec(ctx, "LISTEN "+ReloadChannel); err != nil {
			_ = conn.Close(ctx)
			logging.Warn("notify: listen failed", logging.F("error", err.Error()))
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			continue
		}
		for {
			_, err := conn.WaitForNotification(ctx)
			if err != nil {
				if ctx.Err() != nil {
					_ = conn.Close(ctx)
					return
				}
				_ = conn.Close(ctx)
				logging.Warn("notify: wait failed, reconnecting",
					logging.F("error", err.Error()))
				break
			}
			onReload()
		}
	}
}

// Poll drives the fallback: onReload runs every interval until ctx
// is cancelled. Catches notifications lost while the listener was
// disconnected.
func Poll(ctx context.Context, interval time.Duration, onReload func()) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			onReload()
		}
	}
}
