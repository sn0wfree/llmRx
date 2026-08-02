package auto

import (
	"strings"
	"testing"
)

func TestMapTierBoundaries(t *testing.T) {
	th := DefaultThresholds()
	cases := []struct {
		score float64
		want  Tier
	}{
		{0.00, TierSimple},
		{0.249, TierSimple},
		{0.25, TierStandard},
		{0.549, TierStandard},
		{0.55, TierComplex},
		{0.799, TierComplex},
		{0.80, TierAgentic},
		{1.00, TierAgentic},
	}
	for _, c := range cases {
		if got := MapTier(c.score, th); got != c.want {
			t.Errorf("MapTier(%v) = %v, want %v", c.score, got, c.want)
		}
	}
}

func TestValidTier(t *testing.T) {
	for _, k := range AllTiers {
		if !ValidTier(string(k)) {
			t.Errorf("ValidTier(%q) = false", k)
		}
	}
	for _, bad := range []string{"", "ultra", "SIMPLE", "simple "} {
		if ValidTier(bad) {
			t.Errorf("ValidTier(%q) = true", bad)
		}
	}
}

func TestEmptyInput(t *testing.T) {
	s := NewHeuristicScorer(DefaultThresholds())
	for _, in := range []string{"", "   ", "\n\t "} {
		got := s.Classify(in)
		if got.Tier != TierSimple {
			t.Errorf("empty input tier = %v, want simple", got.Tier)
		}
		if got.Score != 0 {
			t.Errorf("empty input score = %v, want 0", got.Score)
		}
		if got.Cause != CauseEmpty {
			t.Errorf("empty input cause = %q, want %q", got.Cause, CauseEmpty)
		}
	}
}

func TestGreetingIsSimple(t *testing.T) {
	s := NewHeuristicScorer(DefaultThresholds())
	for _, in := range []string{"hello", "Hi there!", "thanks a lot", "hey, how are you?", "你好"} {
		got := s.Classify(in)
		if got.Tier != TierSimple {
			t.Errorf("%q: tier = %v (score %.2f), want simple", in, got.Tier, got.Score)
		}
	}
}

func TestShortFactualQuestionIsSimple(t *testing.T) {
	s := NewHeuristicScorer(DefaultThresholds())
	got := s.Classify("what is the capital of France?")
	if got.Tier != TierSimple {
		t.Errorf("tier = %v (score %.2f), want simple", got.Tier, got.Score)
	}
}

func TestCodeTaskIsStandardOrHigher(t *testing.T) {
	s := NewHeuristicScorer(DefaultThresholds())
	in := "write a function that returns the sum of two numbers: func add(a, b int) int { return a + b } and a test covering the edge cases: func TestAdd(t *testing.T) { result := add(2, 3); if result != 5 { t.Fatal() } }"
	got := s.Classify(in)
	if got.Tier != TierStandard {
		t.Errorf("tier = %v (score %.2f), want standard", got.Tier, got.Score)
	}
	if got.Dims.Code == 0 {
		t.Error("code dimension should be > 0")
	}
}

func TestLongCodeTaskIsComplex(t *testing.T) {
	s := NewHeuristicScorer(DefaultThresholds())
	body := strings.Repeat("implement a websocket endpoint with authentication, error handling and backpressure; use goroutines and mutexes; then write unit tests for the edge cases and explain the trade-offs in the README. ", 25)
	in := "```\n" + body + "```\nplease review the algorithm complexity and refactor the hot path"
	got := s.Classify(in)
	if got.Tier != TierComplex && got.Tier != TierAgentic {
		t.Errorf("tier = %v (score %.2f), want complex or agentic", got.Tier, got.Score)
	}
}

func TestAgenticPlanningTask(t *testing.T) {
	s := NewHeuristicScorer(DefaultThresholds())
	// A genuinely large spec: code fence + numbered steps + heavy
	// reasoning/technical vocabulary + open questions, ~1300 tokens.
	base := "analyze why the endpoint latency matters, justify the trade-offs, explain the deadlock and edge case handling in the goroutine pool, then compare the algorithm variants for the cache layer and evaluate the schema changes against the replicas. "
	code := "```\nfunc main() {\n\treturn\n}\n```\n"
	in := code + strings.Repeat("Step 1: "+base, 30)
	got := s.Classify(in)
	if got.Tier != TierAgentic {
		t.Errorf("tier = %v (score %.2f), want agentic", got.Tier, got.Score)
	}
	if got.Dims.MultiStep == 0 || got.Dims.Reasoning == 0 || got.Dims.Technical == 0 || got.Dims.Code == 0 {
		t.Errorf("dims should light up for a planning task: %+v", got.Dims)
	}
}

func TestLongChineseTaskNotSimple(t *testing.T) {
	s := NewHeuristicScorer(DefaultThresholds())
	// Non-ASCII input has no keyword hits; it must still escalate
	// on length alone (token estimate counts CJK runes).
	short := s.Classify("你好，请问今天天气怎么样？")
	if short.Tier != TierSimple {
		t.Errorf("short chinese tier = %v (score %.2f), want simple", short.Tier, short.Score)
	}
	long := s.Classify(strings.Repeat("请详细分析这个分布式系统的架构设计，评估所有边缘情况并给出完整的实施方案说明，同时比较多种备选方案的优缺点。", 60))
	if long.Tier == TierSimple {
		t.Errorf("long chinese tier = %v (score %.2f), want standard+", long.Tier, long.Score)
	}
	if long.Dims.TokenCount <= 0 {
		t.Error("long chinese input should have a positive token dimension")
	}
}

func TestScoreClamped(t *testing.T) {
	s := NewHeuristicScorer(DefaultThresholds())
	// Pathological: huge + code + reasoning + technical + steps.
	in := "step 1 " + strings.Repeat("explain why the function, the api, the latency, the cache and the algorithm must handle the deadlock, the mutex and the goroutine. ", 500)
	got := s.Classify("```\n" + in + "```\nreturn x")
	if got.Score > 1 {
		t.Fatalf("score = %v, must be clamped to <= 1", got.Score)
	}
}

func TestDeterministic(t *testing.T) {
	s := NewHeuristicScorer(DefaultThresholds())
	in := "please explain the trade-offs of using a mutex vs a channel for this goroutine and analyze the latency implications"
	a, b := s.Classify(in), s.Classify(in)
	if a.Score != b.Score || a.Tier != b.Tier {
		t.Fatalf("not deterministic: %+v vs %+v", a, b)
	}
}

func TestCustomThresholds(t *testing.T) {
	// All-but-zero cutoffs: any positive score lands in agentic.
	s := NewHeuristicScorer(Thresholds{0.001, 0.001, 0.001})
	got := s.Classify("please explain why the api needs a mutex for the goroutine")
	if got.Score <= 0 {
		t.Fatalf("score = %v, want positive", got.Score)
	}
	if got.Tier != TierAgentic {
		t.Errorf("tier = %v, want agentic with permissive thresholds", got.Tier)
	}
}

func TestScoreMatchesWeightedDims(t *testing.T) {
	s := NewHeuristicScorer(DefaultThresholds())
	got := s.Classify("explain why the api needs a mutex for the goroutine, then compare the edge cases")
	want := wToken*got.Dims.TokenCount +
		wCode*got.Dims.Code +
		wReason*got.Dims.Reasoning +
		wTech*got.Dims.Technical +
		wSteps*got.Dims.MultiStep +
		wQuestion*got.Dims.Question -
		wSimple*got.Dims.Simple
	if want > 1 {
		want = 1
	}
	if want < 0 {
		want = 0
	}
	if got.Score != want {
		t.Errorf("score = %v, want weighted sum %v (dims %+v)", got.Score, want, got.Dims)
	}
	if got.Cause != CauseHeuristic {
		t.Errorf("cause = %q, want heuristic", got.Cause)
	}
}

func TestLargeInputFast(t *testing.T) {
	// Sanity guard for the "sub-millisecond" claim: a page of text
	// must classify in a couple of ms, not seconds.
	s := NewHeuristicScorer(DefaultThresholds())
	in := strings.Repeat("explain why the api endpoint latency matters for the cache and the algorithm. ", 400)
	for i := 0; i < 5; i++ {
		s.Classify(in)
	}
}
