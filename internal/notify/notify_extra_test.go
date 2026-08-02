package notify

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestListen_BadDSNRetriesAndStops: with an unreachable database the
// listener must keep retrying (logged, not crashing) and exit cleanly
// when the context is cancelled.
func TestListen_BadDSNRetriesAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var calls int32
	done := make(chan struct{})
	go func() {
		Listen(ctx, "postgres://u:x@127.0.0.1:1/nope?sslmode=disable&connect_timeout=1", func() {
			atomic.AddInt32(&calls, 1)
		})
		close(done)
	}()

	// Let it hit a few connect failures, then cancel.
	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Listen did not stop after context cancel")
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("reload callback fired %d times with no DB, want 0", calls)
	}
}

// TestListen_CancelledBeforeStart: cancelling before any connect
// attempt returns immediately.
func TestListen_CancelledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		Listen(ctx, "postgres://u:x@127.0.0.1:1/nope", func() {})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Listen did not exit for already-cancelled ctx")
	}
}

// TestPoll_StopsOnCancel: the polling loop must stop when the
// context is cancelled (no goroutine leak).
func TestPoll_StopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Poll(ctx, 10*time.Millisecond, func() {})
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Poll did not stop after context cancel")
	}
}
