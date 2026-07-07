package ratelimit

import (
	"sync"
	"testing"
	"time"
)

// TestLimiter_RPMAllowsThenBlocks verifies that requests within RPM
// pass and the (RPM+1)th request is rejected.
func TestLimiter_RPMAllowsThenBlocks(t *testing.T) {
	l := New()
	for i := 0; i < 3; i++ {
		ok, reason := l.Allow(1, 3, 0, 0)
		if !ok {
			t.Fatalf("request %d should be allowed: %s", i, reason)
		}
	}
	ok, reason := l.Allow(1, 3, 0, 0)
	if ok {
		t.Fatal("4th request should be blocked under rpm=3")
	}
	if reason != "rpm exceeded" {
		t.Fatalf("reason: %s", reason)
	}
}

func TestLimiter_TPMRejectsByTokenCount(t *testing.T) {
	l := New()
	ok, _ := l.Allow(1, 0, 100, 60)
	if !ok {
		t.Fatal("first request should pass under tpm=100")
	}
	ok, reason := l.Allow(1, 0, 100, 60)
	if ok {
		t.Fatal("second request should be blocked: projected 120 > 100")
	}
	if reason != "tpm exceeded" {
		t.Fatalf("reason: %s", reason)
	}
}

func TestLimiter_AccountAddsToLastBucket(t *testing.T) {
	l := New()
	l.Allow(1, 0, 100, 10)
	// Account 50 completion tokens to the same key; total is now 60,
	// still under the 100 cap.
	ok, _ := l.Allow(2, 0, 100, 0) // different key
	if !ok {
		t.Fatal("unrelated key should pass")
	}
	// Account bumps the previous bucket's tokens.
	l.Account(1, 50)
	// Now key 1 has used 60 (10 + 50). One more req with 30 puts us
	// at 90, still fine.
	ok, _ = l.Allow(1, 0, 100, 30)
	if !ok {
		t.Fatal("request after Account should still pass")
	}
	// Next request with 30 puts us at 120 > 100, must fail.
	ok, _ = l.Allow(1, 0, 100, 30)
	if ok {
		t.Fatal("after projected 120 should fail")
	}
}

func TestLimiter_WindowSlides(t *testing.T) {
	now := time.Now()
	var clockMu sync.Mutex
	clock := now
	l := New()
	l.SetNow(func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	})

	// 2 RPM cap; use it up at t=0.
	for i := 0; i < 2; i++ {
		l.Allow(1, 2, 0, 0)
	}
	if ok, _ := l.Allow(1, 2, 0, 0); ok {
		t.Fatal("should be over cap at t=0")
	}
	// Move clock forward 61s; entries should evict.
	clockMu.Lock()
	clock = now.Add(61 * time.Second)
	clockMu.Unlock()
	ok, _ := l.Allow(1, 2, 0, 0)
	if !ok {
		t.Fatal("after window slide, cap should be cleared")
	}
}

func TestLimiter_ResetAndKeyIsolation(t *testing.T) {
	l := New()
	l.Allow(1, 1, 0, 0)
	l.Allow(2, 1, 0, 0)
	if got := l.TrackedKeys(); got != 2 {
		t.Fatalf("tracked: %d", got)
	}
	if ok, _ := l.Allow(1, 1, 0, 0); ok {
		t.Fatal("key 1 over cap")
	}
	if ok, _ := l.Allow(2, 1, 0, 0); ok {
		t.Fatal("key 2 over cap")
	}
	l.Reset()
	if got := l.TrackedKeys(); got != 0 {
		t.Fatalf("after Reset: %d", got)
	}
	if ok, _ := l.Allow(1, 1, 0, 0); !ok {
		t.Fatal("post-reset request should pass")
	}
}

// TestLimiter_ConcurrentSafe is a smoke test for the data-race
// detector; the race flag at run time will fail if any goroutine
// sees torn state.
func TestLimiter_ConcurrentSafe(t *testing.T) {
	l := New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				l.Allow(int64(id%10), 0, 0, 1)
				l.Account(int64(id%10), 1)
			}
		}(i)
	}
	wg.Wait()
}