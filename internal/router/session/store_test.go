package session

import (
	"sync"
	"testing"
	"time"
)

func TestSetGetRoundTrip(t *testing.T) {
	s := New(Config{TTL: time.Hour, MaxEntries: 10})
	s.Set("abc", "gpt-4o", "complex", 7)
	p, ok := s.Get("abc")
	if !ok {
		t.Fatal("expected pin")
	}
	if p.Model != "gpt-4o" || p.Tier != "complex" || p.ChannelID != 7 {
		t.Fatalf("pin = %+v", p)
	}
	if s.Count() != 1 {
		t.Fatalf("count = %d, want 1", s.Count())
	}
}

func TestGetMissing(t *testing.T) {
	s := New(Config{TTL: time.Hour, MaxEntries: 10})
	if _, ok := s.Get("nope"); ok {
		t.Fatal("missing pin must not be found")
	}
	if _, ok := s.Get(""); ok {
		t.Fatal("empty session id must not be found")
	}
}

func TestGetExpired(t *testing.T) {
	s := New(Config{TTL: 10 * time.Millisecond, MaxEntries: 10})
	s.Set("abc", "gpt-4o", "complex", 7)
	time.Sleep(20 * time.Millisecond)
	if _, ok := s.Get("abc"); ok {
		t.Fatal("expired pin must not be found")
	}
	if s.Count() != 0 {
		t.Fatalf("expired pin should be deleted on access, count=%d", s.Count())
	}
}

func TestSweep(t *testing.T) {
	s := New(Config{TTL: 10 * time.Millisecond, MaxEntries: 10})
	s.Set("a", "m1", "simple", 1)
	s.Set("b", "m2", "simple", 2)
	time.Sleep(20 * time.Millisecond)
	s.Set("c", "m3", "simple", 3) // fresh
	s.Sweep()
	if s.Count() != 1 {
		t.Fatalf("after sweep count=%d, want 1", s.Count())
	}
	if _, ok := s.Get("c"); !ok {
		t.Fatal("fresh pin must survive sweep")
	}
}

func TestMaxEntriesEvictsOldest(t *testing.T) {
	s := New(Config{TTL: time.Hour, MaxEntries: 2})
	s.Set("a", "m1", "simple", 1)
	s.Set("b", "m2", "simple", 2)
	// "a" expires first (inserted first with the same TTL).
	s.Set("c", "m3", "simple", 3)
	if s.Count() != 2 {
		t.Fatalf("count=%d, want 2", s.Count())
	}
	if _, ok := s.Get("a"); ok {
		t.Fatal("oldest-expiring pin must be evicted")
	}
	if _, ok := s.Get("b"); !ok {
		t.Fatal("b must survive")
	}
	if _, ok := s.Get("c"); !ok {
		t.Fatal("c must survive")
	}
}

func TestSetRefreshesExpiry(t *testing.T) {
	s := New(Config{TTL: time.Hour, MaxEntries: 10})
	s.Set("a", "m1", "simple", 1)
	p1, _ := s.Get("a")
	time.Sleep(2 * time.Millisecond)
	s.Set("a", "m1", "simple", 1) // refresh
	p2, _ := s.Get("a")
	if !p2.ExpiresAt.After(p1.ExpiresAt) {
		t.Fatal("Set must refresh the expiry")
	}
}

func TestDelete(t *testing.T) {
	s := New(Config{TTL: time.Hour, MaxEntries: 10})
	s.Set("a", "m1", "simple", 1)
	s.Delete("a")
	if _, ok := s.Get("a"); ok {
		t.Fatal("deleted pin must be gone")
	}
}

func TestReset(t *testing.T) {
	s := New(Config{TTL: time.Hour, MaxEntries: 10})
	s.Set("a", "m1", "simple", 1)
	s.Reset()
	if s.Count() != 0 {
		t.Fatalf("after reset count=%d, want 0", s.Count())
	}
}

func TestHashDeterministicAndDistinct(t *testing.T) {
	h1 := Hash("system", "user msg")
	h2 := Hash("system", "user msg")
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("hash not deterministic: %s vs %s", h1, h2)
	}
	h3 := Hash("system", "user msg 2")
	if h1 == h3 {
		t.Fatal("different first-user must hash differently")
	}
}

func TestSnapshotIsCopy(t *testing.T) {
	s := New(Config{TTL: time.Hour, MaxEntries: 10})
	s.Set("a", "m1", "simple", 1)
	snap := s.Snapshot()
	snap["a"] = Pin{Model: "mutated"}
	if p, _ := s.Get("a"); p.Model != "m1" {
		t.Fatal("snapshot must not alias internal state")
	}
}

func TestConcurrent(t *testing.T) {
	s := New(Config{TTL: time.Hour, MaxEntries: 1000})
	var wg sync.WaitGroup
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			id := string(rune('a' + g%5))
			for i := 0; i < 500; i++ {
				s.Set(id, "m1", "simple", 1)
				_, _ = s.Get(id)
				_ = s.Count()
				_ = s.Snapshot()
			}
		}(g)
	}
	wg.Wait()
	if s.Count() > 5 {
		t.Fatalf("count=%d, want <= 5", s.Count())
	}
}
