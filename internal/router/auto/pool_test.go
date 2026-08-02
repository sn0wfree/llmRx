package auto

import (
	"testing"

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
