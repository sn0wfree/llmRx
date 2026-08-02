package auto

import (
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/modelmeta"
	"github.com/sn0wfree/llmRx/internal/router/thompson"
)

func TestArmKeyRoundTrip(t *testing.T) {
	key := ArmKey("complex", "gpt-4o")
	if key != "complex:gpt-4o" {
		t.Fatalf("ArmKey = %q", key)
	}
	tier, model := ParseArmKey(key)
	if tier != "complex" || model != "gpt-4o" {
		t.Fatalf("ParseArmKey = %q, %q", tier, model)
	}
}

func TestParseArmKeyNoColon(t *testing.T) {
	tier, model := ParseArmKey("nocolon")
	if tier != "" || model != "nocolon" {
		t.Fatalf("ParseArmKey = %q, %q", tier, model)
	}
}

func TestSelectEmpty(t *testing.T) {
	p := NewPool(thompson.New(thompson.Config{Seed: 1}))
	got := p.Select("simple", nil)
	if got.Model != "" || got.Key != "" || got.Score != 0 {
		t.Fatalf("empty select = %+v", got)
	}
}

// TestSelectColdStartCheapest: with no observations the cold-start
// gate preserves the input (cost) order, so the cheapest candidate
// wins.
func TestSelectColdStartCheapest(t *testing.T) {
	p := NewPool(thompson.New(thompson.Config{Seed: 3, MinSamplesPerChannel: 5}))
	got := p.Select("simple", []string{"deepseek-chat", "gpt-4o", "claude-3-5-sonnet"})
	if got.Model != "deepseek-chat" {
		t.Fatalf("cold start should pick the cheapest, got %+v", got)
	}
	if got.Score != 0 {
		t.Fatalf("cold start score should be 0, got %v", got.Score)
	}
}

// TestSelectWarmPicksQuality: once arms clear the gate, the
// high-quality model wins even when it sits later in the cost
// order — this is the learning loop working.
func TestSelectWarmPicksQuality(t *testing.T) {
	s := thompson.New(thompson.Config{Seed: 7, MinSamplesPerChannel: 5})
	for i := 0; i < 30; i++ {
		s.RecordArmSuccess(ArmKey("simple", "gpt-4o-mini"))   // cheap + good
		s.RecordArmFailure(ArmKey("simple", "deepseek-chat")) // cheapest + bad
	}
	p := NewPool(s)
	wins := 0
	const N = 100
	for i := 0; i < N; i++ {
		got := p.Select("simple", []string{"deepseek-chat", "gpt-4o-mini"})
		if got.Model == "gpt-4o-mini" {
			wins++
		}
	}
	if wins < int(0.99*float64(N)) {
		t.Fatalf("quality arm should dominate: %d/%d", wins, N)
	}
}

// TestFilterContextCandidates: models whose context window cannot
// fit the estimated prompt (+ headroom) are dropped; unknown models
// are kept (lenient).
func TestFilterContextCandidates(t *testing.T) {
	if err := modelmeta.Init(""); err != nil {
		t.Fatalf("modelmeta.Init: %v", err)
	}
	// gpt-4o: 128k context; deepseek-chat: 64k. "unknown-model" has
	// no entry.
	cands := []string{"gpt-4o", "deepseek-chat", "unknown-model"}

	// Tiny prompt: nothing filtered.
	got := FilterContextCandidates(cands, 10)
	if len(got) != 3 {
		t.Fatalf("tiny prompt filtered: %v", got)
	}

	// 150k estimated tokens -> need 180k: both known models dropped.
	got = FilterContextCandidates(cands, 150000)
	if len(got) != 1 || got[0] != "unknown-model" {
		t.Fatalf("huge prompt: want [unknown-model], got %v", got)
	}

	// 70k tokens -> need 84k: deepseek-chat (64k) dropped, gpt-4o kept.
	got = FilterContextCandidates(cands, 70000)
	if len(got) != 2 {
		t.Fatalf("70k prompt: want 2 candidates, got %v", got)
	}
	for _, m := range got {
		if m == "deepseek-chat" {
			t.Fatalf("deepseek-chat should be filtered at 70k: %v", got)
		}
	}
}

// TestFilterContextCandidates_NeverStarves: when every candidate
// would be dropped the input is returned unchanged.
func TestFilterContextCandidates_NeverStarves(t *testing.T) {
	if err := modelmeta.Init(""); err != nil {
		t.Fatalf("modelmeta.Init: %v", err)
	}
	got := FilterContextCandidates([]string{"gpt-4o", "deepseek-chat"}, 500000)
	if len(got) != 2 {
		t.Fatalf("tier must not be starved: %v", got)
	}
}

// TestFilterContextCandidates_EdgeInputs: empty / single-element /
// non-positive token counts pass through untouched.
func TestFilterContextCandidates_EdgeInputs(t *testing.T) {
	if err := modelmeta.Init(""); err != nil {
		t.Fatalf("modelmeta.Init: %v", err)
	}
	if got := FilterContextCandidates(nil, 100); len(got) != 0 {
		t.Fatalf("nil: %v", got)
	}
	if got := FilterContextCandidates([]string{"gpt-4o"}, 100); len(got) != 1 {
		t.Fatalf("single: %v", got)
	}
	if got := FilterContextCandidates([]string{"gpt-4o", "deepseek-chat"}, 0); len(got) != 2 {
		t.Fatalf("zero tokens: %v", got)
	}
}

// TestTokenEstimate: CJK runes count one token each, ASCII ~4/1.
func TestTokenEstimate(t *testing.T) {
	if got := TokenEstimate(strings.Repeat("a", 400)); got != 100 {
		t.Fatalf("ascii: got %d, want 100", got)
	}
	if got := TokenEstimate(strings.Repeat("中", 50)); got != 50 {
		t.Fatalf("cjk: got %d, want 50", got)
	}
}
