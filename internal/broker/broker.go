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
	"errors"
	"log"
	"sync"
)

// BufferSize is the per-subscriber channel capacity. Slow consumers
// exceeding this get dropped.
const BufferSize = 256

// ErrTooManySubscribers is returned by Subscribe when the broker
// already has MaxSubscribers live consumers. Callers should respond
// with HTTP 503 (capacity exceeded) rather than block.
var ErrTooManySubscribers = errors.New("broker: too many subscribers")

// Broker is a fan-out hub for value type T.
type Broker[T any] struct {
	mu             sync.RWMutex
	subs           map[chan T]struct{}
	closed         bool
	maxSubscribers int // 0 = unlimited
}

// New returns an empty broker. A non-zero maxSubscribers caps the
// number of concurrent Subscribe callers; further calls fail with
// ErrTooManySubscribers until existing subscribers Unsubscribe.
// A value of 0 disables the cap.
func New[T any](maxSubscribers int) *Broker[T] {
	return &Broker[T]{
		subs:           make(map[chan T]struct{}),
		maxSubscribers: maxSubscribers,
	}
}

// MaxSubscribers returns the configured hard cap (0 = unlimited).
func (b *Broker[T]) MaxSubscribers() int { return b.maxSubscribers }

// Subscribe registers a new consumer and returns its channel plus
// an unsubscribe function. The channel is closed when the broker
// is closed or the caller invokes the returned func.
//
// If MaxSubscribers > 0 and the cap is reached, Subscribe returns a
// nil channel, ErrTooManySubscribers, and a no-op unsubscribe so the
// caller can fail-fast.
func (b *Broker[T]) Subscribe() (<-chan T, func(), error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		ch := make(chan T)
		close(ch)
		return ch, func() {}, nil
	}
	if b.maxSubscribers > 0 && len(b.subs) >= b.maxSubscribers {
		cur := len(b.subs)
		b.mu.Unlock()
		log.Printf("warn: broker subscriber cap reached (%d/%d)", cur, b.maxSubscribers)
		return nil, func() {}, ErrTooManySubscribers
	}
	ch := make(chan T, BufferSize)
	b.subs[ch] = struct{}{}
	count := len(b.subs)
	b.mu.Unlock()
	if count > 1 && count%64 == 0 {
		log.Printf("info: broker has %d live subscribers (cap=%d)", count, b.maxSubscribers)
	}
	return ch, func() { b.unsubscribe(ch) }, nil
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
