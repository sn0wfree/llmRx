package notify

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestListenReceivesNotification verifies the LISTEN/NOTIFY bridge:
// a pg_notify call triggers the reload callback.
func TestListenReceivesNotification(t *testing.T) {
	dsn := os.Getenv("LLMRX_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set LLMRX_TEST_PG_DSN to run")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := make(chan struct{}, 1)
	go Listen(ctx, dsn, func() {
		select {
		case received <- struct{}{}:
		default:
		}
	})

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// The listener needs a moment to attach; re-send until observed
	// (a NOTIFY sent before LISTEN is silently dropped).
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := conn.Exec(ctx, "SELECT pg_notify('llmrx_reload','')"); err != nil {
			t.Fatalf("pg_notify: %v", err)
		}
		select {
		case <-received:
			return // success
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Fatal("listener never received the notification")
}

// TestPollInterval verifies the polling fallback fires periodically.
func TestPollInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fired := make(chan struct{}, 1)
	go Poll(ctx, 50*time.Millisecond, func() {
		select {
		case fired <- struct{}{}:
		default:
		}
	})

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("poll never fired")
	}
}
