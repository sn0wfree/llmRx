package api

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestLogEmitter_Sampling_DefaultEvery verifies the default rate
// (1) emits the chat.completed line on every call.
func TestLogEmitter_Sampling_DefaultEvery(t *testing.T) {
	e := &LogEmitter{chatLogSampleRate: 1}
	for i := 0; i < 100; i++ {
		if !e.emitChatCompletedSamples() {
			t.Fatalf("rate=1 should emit on call %d", i)
		}
	}
}

// TestLogEmitter_Sampling_Disabled verifies rate=0 never emits.
func TestLogEmitter_Sampling_Disabled(t *testing.T) {
	e := &LogEmitter{chatLogSampleRate: 0}
	for i := 0; i < 100; i++ {
		if e.emitChatCompletedSamples() {
			t.Fatalf("rate=0 should never emit (call %d)", i)
		}
	}
}

// TestLogEmitter_Sampling_OneInN verifies that an N rate emits
// exactly once per N calls. With rate=5, after 100 calls we should
// have exactly 20 emits (at positions 5, 10, 15, ..., 100).
func TestLogEmitter_Sampling_OneInN(t *testing.T) {
	e := &LogEmitter{chatLogSampleRate: 5}
	emits := 0
	for i := 1; i <= 100; i++ {
		if e.emitChatCompletedSamples() {
			emits++
		}
	}
	if emits != 20 {
		t.Fatalf("rate=5 expected 20 emits, got %d", emits)
	}
	// First emit should be at count=5, not 1.
	e2 := &LogEmitter{chatLogSampleRate: 5}
	first := 0
	for i := 1; i <= 5; i++ {
		if e2.emitChatCompletedSamples() {
			first = i
		}
	}
	if first != 5 {
		t.Fatalf("first emit should be at count=5, got %d", first)
	}
}

// TestLogEmitter_Sampling_SetAfterConstruct verifies the runtime
// setter picks up the new rate, and resets the counter so the
// next sampled call emits immediately.
func TestLogEmitter_Sampling_SetAfterConstruct(t *testing.T) {
	e := &LogEmitter{chatLogSampleRate: 5}
	// burn 4 calls: counter is now 4, no emit yet.
	for i := 0; i < 4; i++ {
		if e.emitChatCompletedSamples() {
			t.Fatal("should not emit before 5th call")
		}
	}
	// Switch to rate=1: next call must emit immediately.
	e.SetChatLogSampleRate(1)
	if !e.emitChatCompletedSamples() {
		t.Fatal("after switching to rate=1, first call must emit")
	}
	// Switch to rate=0: never emit again.
	e.SetChatLogSampleRate(0)
	if e.emitChatCompletedSamples() {
		t.Fatal("rate=0 must never emit")
	}
	// Switch to rate=100: counter reset, so 100th call emits.
	e.SetChatLogSampleRate(100)
	for i := 1; i < 100; i++ {
		if e.emitChatCompletedSamples() {
			t.Fatalf("rate=100 emitted early at call %d", i)
		}
	}
	if !e.emitChatCompletedSamples() {
		t.Fatal("rate=100 should emit at call 100")
	}
}

// TestLogEmitter_Sampling_NegativeOrZero verifies the setter clamps
// weird inputs to the documented semantics.
func TestLogEmitter_Sampling_NegativeOrZero(t *testing.T) {
	e := &LogEmitter{}
	for _, n := range []int{-1, -100, 0} {
		e.SetChatLogSampleRate(n)
		if e.emitChatCompletedSamples() {
			t.Fatalf("rate=%d should disable emission", n)
		}
	}
}

// TestLogEmitter_Sampling_Concurrent verifies the sampler is
// race-free under concurrent Emit calls. With rate=N, exactly
// floor(iterations/N) emits should occur (within a small tolerance
// since the counter is monotonic).
func TestLogEmitter_Sampling_Concurrent(t *testing.T) {
	e := &LogEmitter{chatLogSampleRate: 100}
	const goroutines = 8
	const perG = 1000
	var emits int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				if e.emitChatCompletedSamples() {
					atomic.AddInt64(&emits, 1)
				}
			}
		}()
	}
	wg.Wait()
	got := atomic.LoadInt64(&emits)
	// 8 goroutines * 1000 calls = 8000 calls, rate=100 → 80 expected.
	// Allow ±2 to absorb boundary effects on the last call.
	if got < 78 || got > 82 {
		t.Fatalf("rate=100, 8000 calls: expected ~80 emits, got %d", got)
	}
}
