// Package broker provides a tiny in-process pub/sub used to fan
// out log events from the chat pipeline to SSE subscribers.
//
// Subscribers receive values on a buffered channel. If a subscriber
// is slow and the buffer fills, it is dropped (the next Subscribe
// call yields a fresh channel) and a warning is logged. The design
// is intentionally simple: it runs inside a single process, has no
// persistence, and is not safe to share across processes.
package broker

import (
	"log"
	"sync"
)

// BufferSize is the per-subscriber channel capacity. Slow consumers
// exceeding this get dropped.
const BufferSize = 256

// Broker is a fan-out hub for value type T.
type Broker[T any] struct {
	mu     sync.RWMutex
	subs   map[chan T]struct{}
	closed bool
}

// New returns an empty broker.
func New[T any]() *Broker[T] {
	return &Broker[T]{subs: make(map[chan T]struct{})}
}

// Subscribe registers a new consumer and returns its channel plus
// an unsubscribe function. The channel is closed when the broker
// is closed or the caller invokes the returned func.
func (b *Broker[T]) Subscribe() (<-chan T, func()) {
	ch := make(chan T, BufferSize)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() { b.unsubscribe(ch) }
}

func (b *Broker[T]) unsubscribe(ch chan T) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		close(ch)
	}
}

// Publish delivers v to every subscriber. Slow subscribers (full
// buffer) are dropped with a warning; the publisher never blocks.
func (b *Broker[T]) Publish(v T) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return 0
	}
	delivered := 0
	for ch := range b.subs {
		select {
		case ch <- v:
			delivered++
		default:
			log.Printf("warn: broker slow subscriber dropped (buf=%d)", BufferSize)
		}
	}
	return delivered
}

// SubscriberCount returns the number of live subscribers. Useful
// for tests and health checks.
func (b *Broker[T]) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Close closes every subscriber channel. Publish after Close is a
// no-op. Subscribers must be ready to receive ErrClosed values
// from their channels if they are still reading when Close runs.
func (b *Broker[T]) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for ch := range b.subs {
		close(ch)
	}
	b.subs = map[chan T]struct{}{}
}
