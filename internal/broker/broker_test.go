package broker

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublishFanOut(t *testing.T) {
	b := New[int](0)
	defer b.Close()

	c1, _, err := b.Subscribe()
	if err != nil {
		t.Fatalf("subscribe c1: %v", err)
	}
	c2, _, err := b.Subscribe()
	if err != nil {
		t.Fatalf("subscribe c2: %v", err)
	}
	if got := b.SubscriberCount(); got != 2 {
		t.Fatalf("subs: %d", got)
	}

	n := b.Publish(42)
	if n != 2 {
		t.Fatalf("delivered: %d", n)
	}
	if v := <-c1; v != 42 {
		t.Fatalf("c1: %d", v)
	}
	if v := <-c2; v != 42 {
		t.Fatalf("c2: %d", v)
	}
}

func TestUnsubscribeClosesChannel(t *testing.T) {
	b := New[string](0)
	c, unsub, err := b.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	unsub()
	_, ok := <-c
	if ok {
		t.Fatal("expected closed channel")
	}
	if got := b.SubscriberCount(); got != 0 {
		t.Fatalf("subs: %d", got)
	}
}

func TestSlowSubscriberDropped(t *testing.T) {
	b := New[int](0)
	defer b.Close()
	_, _, _ = b.Subscribe()
	// Fill the buffer (BufferSize=256) without reading.
	for i := 0; i < BufferSize; i++ {
		if n := b.Publish(i); n != 1 {
			t.Fatalf("publish %d: delivered=%d", i, n)
		}
	}
	// One more should be dropped (no panic, no block).
	if n := b.Publish(999); n != 0 {
		t.Fatalf("expected 0 delivered when buffer full, got %d", n)
	}
}

func TestPublishAfterClose(t *testing.T) {
	b := New[int](0)
	b.Close()
	if n := b.Publish(1); n != 0 {
		t.Fatalf("post-close publish: %d", n)
	}
}

func TestSubscribeAfterClose(t *testing.T) {
	b := New[int](0)
	b.Close()
	c, _, _ := b.Subscribe()
	_, ok := <-c
	if ok {
		t.Fatal("expected closed channel when subscribing to closed broker")
	}
}

func TestConcurrent(t *testing.T) {
	b := New[int](0)
	defer b.Close()
	var wg sync.WaitGroup
	var received int64
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, unsub, err := b.Subscribe()
			if err != nil {
				t.Errorf("subscribe: %v", err)
				return
			}
			defer unsub()
			for range c {
				atomic.AddInt64(&received, 1)
			}
		}()
	}
	for i := 0; i < 1000; i++ {
		b.Publish(i)
	}
	// Let consumers drain.
	time.Sleep(50 * time.Millisecond)
	// Close will close all channels, range loops exit, wg completes.
	b.Close()
	wg.Wait()
}

func TestMaxSubscribersCap(t *testing.T) {
	b := New[int](2)
	defer b.Close()

	if got := b.MaxSubscribers(); got != 2 {
		t.Fatalf("MaxSubscribers: %d", got)
	}
	c1, unsub1, err := b.Subscribe()
	if err != nil {
		t.Fatalf("subscribe c1: %v", err)
	}
	_, unsub2, err := b.Subscribe()
	if err != nil {
		t.Fatalf("subscribe c2: %v", err)
	}

	// 3rd should be rejected without blocking.
	ch3, unsub3, err := b.Subscribe()
	if !errors.Is(err, ErrTooManySubscribers) {
		t.Fatalf("expected ErrTooManySubscribers, got %v", err)
	}
	if ch3 != nil {
		t.Fatal("expected nil channel when over cap")
	}
	unsub3() // must be safe to call even on rejected subs

	// Free a slot via proper unsubscribe.
	unsub2()
	if got := b.SubscriberCount(); got != 1 {
		t.Fatalf("subs after unsub2: %d", got)
	}
	c3, _, err := b.Subscribe()
	if err != nil {
		t.Fatalf("subscribe after free: %v", err)
	}
	b.Publish(7)
	if v := <-c3; v != 7 {
		t.Fatalf("c3: %d", v)
	}
	if v := <-c1; v != 7 {
		t.Fatalf("c1: %d", v)
	}
	unsub1()
}

func TestMaxSubscribersUnlimited(t *testing.T) {
	b := New[int](0)
	defer b.Close()
	if got := b.MaxSubscribers(); got != 0 {
		t.Fatalf("max: %d", got)
	}
	// Should never reject.
	for i := 0; i < 50; i++ {
		if _, _, err := b.Subscribe(); err != nil {
			t.Fatalf("unlimited sub %d: %v", i, err)
		}
	}
}

