package auto

import (
	"testing"
)

func TestNeedsIsEmpty(t *testing.T) {
	tests := []struct {
		name string
		n    Needs
		want bool
	}{
		{"empty", Needs{}, true},
		{"tools", Needs{Tools: true}, false},
		{"vision", Needs{Vision: true}, false},
		{"json", Needs{JSONMode: true}, false},
		{"reasoning", Needs{Reasoning: true}, false},
		{"all", Needs{Tools: true, Vision: true, JSONMode: true, Reasoning: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.n.IsEmpty(); got != tt.want {
				t.Errorf("IsEmpty() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNopLearnedRouter(t *testing.T) {
	r := nopLearnedRouter{}
	if r.Loaded() {
		t.Error("nop should not be loaded")
	}
	scores := r.Infer("hello", []string{"gpt-4o", "claude-3-5-sonnet"}, Needs{})
	if scores != nil {
		t.Error("nop should return nil scores")
	}
	if err := r.Reload(); err != nil {
		t.Errorf("nop Reload should not error: %v", err)
	}
}

func TestMockLearnedRouterUniform(t *testing.T) {
	r := NewMockLearnedRouter()
	if !r.Loaded() {
		t.Error("mock should be loaded")
	}
	candidates := []string{"gpt-4o", "claude-3-5-sonnet", "deepseek-chat"}
	scores := r.Infer("hello", candidates, Needs{})
	if len(scores) != 3 {
		t.Fatalf("expected 3 scores, got %d", len(scores))
	}
	for _, m := range candidates {
		if scores[m] != 0.5 {
			t.Errorf("expected uniform 0.5 for %s, got %f", m, scores[m])
		}
	}
}

func TestMockLearnedRouterWithScores(t *testing.T) {
	r := NewMockLearnedRouterWithScores(map[string]float64{
		"gpt-4o":          0.9,
		"claude-3-5-sonnet": 0.7,
	})
	candidates := []string{"gpt-4o", "claude-3-5-sonnet", "deepseek-chat"}
	scores := r.Infer("hello", candidates, Needs{})
	if scores["gpt-4o"] != 0.9 {
		t.Errorf("expected 0.9 for gpt-4o, got %f", scores["gpt-4o"])
	}
	if scores["claude-3-5-sonnet"] != 0.7 {
		t.Errorf("expected 0.7 for claude-3-5-sonnet, got %f", scores["claude-3-5-sonnet"])
	}
	if scores["deepseek-chat"] != 0.5 {
		t.Errorf("expected 0.5 for unknown model, got %f", scores["deepseek-chat"])
	}
}

func TestMockLearnedRouterNotLoaded(t *testing.T) {
	r := NewMockLearnedRouter()
	r.SetLoaded(false)
	if r.Loaded() {
		t.Error("should not be loaded after SetLoaded(false)")
	}
	scores := r.Infer("hello", []string{"gpt-4o"}, Needs{})
	if scores != nil {
		t.Error("should return nil when not loaded")
	}
}

func TestDefaultFusionWeights(t *testing.T) {
	w := DefaultFusionWeights()
	total := w.Ts + w.Learned + w.Cost
	if total != 1.0 {
		t.Errorf("weights should sum to 1.0, got %f", total)
	}
	if w.Ts != 0.4 || w.Learned != 0.4 || w.Cost != 0.2 {
		t.Errorf("unexpected weights: %+v", w)
	}
}

func TestNormalizeCosts(t *testing.T) {
	candidates := []string{"gpt-4o", "claude-3-5-sonnet"}
	scores := normalizeCosts(candidates)
	if len(scores) != 2 {
		t.Fatalf("expected 2 scores, got %d", len(scores))
	}
	// Both should be in [0, 1]
	for _, s := range scores {
		if s < 0 || s > 1 {
			t.Errorf("score %f out of [0, 1]", s)
		}
	}
}

func TestNormalizeCostsEmpty(t *testing.T) {
	scores := normalizeCosts(nil)
	if scores != nil {
		t.Error("expected nil for empty candidates")
	}
}

func TestDecisionLearnedUsed(t *testing.T) {
	d := Decision{Tier: "simple", Cause: "learned_fallback", LearnedUsed: false}
	stats := NewStats()
	stats.Record(d)
	snap := stats.Snapshot()
	if snap.LearnedHits != 0 {
		t.Errorf("expected 0 learned hits, got %d", snap.LearnedHits)
	}
	if snap.LearnedMisses != 1 {
		t.Errorf("expected 1 learned miss, got %d", snap.LearnedMisses)
	}

	d2 := Decision{Tier: "complex", Cause: "ts_with_learned", LearnedUsed: true}
	stats.Record(d2)
	snap2 := stats.Snapshot()
	if snap2.LearnedHits != 1 {
		t.Errorf("expected 1 learned hit, got %d", snap2.LearnedHits)
	}
}

func TestStatsReset(t *testing.T) {
	stats := NewStats()
	stats.Record(Decision{Tier: "simple", LearnedUsed: true})
	stats.Record(Decision{Cause: "learned_fallback"})
	snap := stats.Snapshot()
	if snap.LearnedHits != 1 || snap.LearnedMisses != 1 {
		t.Errorf("before reset: hits=%d misses=%d", snap.LearnedHits, snap.LearnedMisses)
	}
	stats.Reset()
	snap2 := stats.Snapshot()
	if snap2.LearnedHits != 0 || snap2.LearnedMisses != 0 {
		t.Errorf("after reset: hits=%d misses=%d", snap2.LearnedHits, snap2.LearnedMisses)
	}
}
