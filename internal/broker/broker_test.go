package broker

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublishFanOut(t *testing.T) {
	b := New[int]()
	defer b.Close()

	c1, _ := b.Subscribe()
	c2, _ := b.Subscribe()
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
	b := New[string]()
	c, unsub := b.Subscribe()
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
	b := New[int]()
	defer b.Close()
	_, _ = b.Subscribe()
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
	b := New[int]()
	b.Close()
	if n := b.Publish(1); n != 0 {
		t.Fatalf("post-close publish: %d", n)
	}
}

func TestSubscribeAfterClose(t *testing.T) {
	b := New[int]()
	b.Close()
	c, _ := b.Subscribe()
	_, ok := <-c
	if ok {
		t.Fatal("expected closed channel when subscribing to closed broker")
	}
}

func TestConcurrent(t *testing.T) {
	b := New[int]()
	defer b.Close()
	var wg sync.WaitGroup
	var received int64
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, unsub := b.Subscribe()
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
