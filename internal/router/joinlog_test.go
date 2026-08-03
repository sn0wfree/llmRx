package router

import (
	"sync"
	"testing"
)

// TestRouterEngine_JoinLog covers the empty / single / multi cases
// of the RouterLog path (used by emitLog "path" field, which is
// emitted on every request).
func TestRouterEngine_JoinLog(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
		want  string
	}{
		{"empty", nil, ""},
		{"single", []string{"L1(static)"}, "L1(static)"},
		{"two", []string{"L1(static)", "select=A"}, "L1(static) → select=A"},
		{"five", []string{"L1(static)", "L2(breaker)", "L3(cost)", "L4(intent=code)", "L5(thompson)", "select=A"},
			"L1(static) → L2(breaker) → L3(cost) → L4(intent=code) → L5(thompson) → select=A"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := joinLog(tc.parts)
			if got != tc.want {
				t.Errorf("joinLog(%v) = %q, want %q", tc.parts, got, tc.want)
			}
		})
	}
}

// TestRouterEngine_JoinLog_Concurrent ensures joinLog is safe
// (no shared mutable state) under concurrent calls. The previous
// `s +=` form had no shared state either, but the strings.Builder
// path is now the only one — assert it stays that way.
func TestRouterEngine_JoinLog_Concurrent(t *testing.T) {
	const goroutines = 32
	const iters = 1000
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				if got := joinLog([]string{"a", "b", "c", "d", "e"}); got != "a → b → c → d → e" {
					t.Errorf("joinLog concurrent: %q", got)
				}
			}
		}()
	}
	wg.Wait()
}
