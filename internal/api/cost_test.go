package api

import (
	"math"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
)

func TestRealCost_TieredLongContext(t *testing.T) {
	cc := &CostCalculator{}
	mc := &model.ModelCost{
		InputPricePer1M:           1.0,
		OutputPricePer1M:          3.0,
		InputPricePer1MAbove128K:  2.0,
		InputPricePer1MAbove200K:  4.0,
		OutputPricePer1MAbove128K: 6.0,
	}

	// 250k prompt tokens: 128k @ $1 + 72k @ $2 + 50k @ $4.
	u := provider.Usage{PromptTokens: 250_000}
	got := cc.RealCost(mc, u)
	want := 128000/1e6*1.0 + 72000/1e6*2.0 + 50000/1e6*4.0
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("tiered input: got %.6f want %.6f", got, want)
	}

	// 150k completion: 128k @ $3 + 22k @ $6.
	u = provider.Usage{CompletionTokens: 150_000}
	got = cc.RealCost(mc, u)
	want = 128000/1e6*3.0 + 22000/1e6*6.0
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("tiered output: got %.6f want %.6f", got, want)
	}
}

func TestRealCost_ReasoningTokens(t *testing.T) {
	cc := &CostCalculator{}
	mc := &model.ModelCost{OutputPricePer1M: 1.0, ReasoningPricePer1M: 4.0}
	// 1M completion, 200k of it reasoning: 800k @ $1 + 200k @ $4 = $1.60.
	u := provider.Usage{
		CompletionTokens:        1_000_000,
		CompletionTokensDetails: &provider.CompletionTokensDetails{ReasoningTokens: 200_000},
	}
	got := cc.RealCost(mc, u)
	want := 800000/1e6*1.0 + 200000/1e6*4.0
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("reasoning: got %.6f want %.6f", got, want)
	}

	// Reasoning tokens clamp to completion.
	u.CompletionTokensDetails.ReasoningTokens = 2_000_000
	got = cc.RealCost(mc, u)
	want = 1_000_000 / 1e6 * 4.0
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("reasoning clamp: got %.6f want %.6f", got, want)
	}
}

func TestRealCost_CacheWriteDiscount(t *testing.T) {
	cc := &CostCalculator{}
	mc := &model.ModelCost{
		InputPricePer1M:      2.0,
		CacheWritePricePer1M: 0.5, // upstream discounts cache-creation
		CacheReadPricePer1M:  0.2,
	}
	// 1M prompt, 400k cached: uncached 600k @ $0.5 + cached 400k @ $0.2.
	u := provider.Usage{
		PromptTokens:        1_000_000,
		PromptTokensDetails: &provider.PromptTokensDetails{CachedTokens: 400_000},
	}
	got := cc.RealCost(mc, u)
	want := 600000/1e6*0.5 + 400000/1e6*0.2
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("cache write: got %.6f want %.6f", got, want)
	}
}

func TestRealCost_Images(t *testing.T) {
	cc := &CostCalculator{}
	mc := &model.ModelCost{InputPricePer1M: 1.0, OutputPricePer1M: 1.0, ImagePrice: 0.02}
	u := provider.Usage{PromptTokens: 1000, CompletionTokens: 1000, Images: 3}
	got := cc.RealCost(mc, u)
	want := 1000/1e6*1.0 + 1000/1e6*1.0 + 3*0.02
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("images: got %.6f want %.6f", got, want)
	}
}

func TestRealCost_FreeModel(t *testing.T) {
	cc := &CostCalculator{}
	mc := &model.ModelCost{} // all zeros = free
	u := provider.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}
	if got := cc.RealCost(mc, u); got != 0 {
		t.Fatalf("free model should cost 0, got %f", got)
	}
}
