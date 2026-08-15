package api

import (
	"context"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
)

type CostCalculator struct {
	markup func() float64
}

func NewCostCalculator(markupFn func() float64) *CostCalculator {
	return &CostCalculator{markup: markupFn}
}

// RealCost computes the upstream real cost for one request from the
// model's rate card and the actual token usage. The rate card is the
// SOLE source of pricing; channel prices no longer participate.
//
// Formula (all prices per 1M tokens unless noted):
//
//	prompt_uncached = prompt − cached
//	real = tieredInput(prompt_uncached)                       // base/128k/200k bands
//	     + cached / 1M * cache_read_price
//	     + tieredOutput(completion − reasoning)               // output base/tiers
//	     + reasoning / 1M * reasoning_price                   // when set
//	     + images * image_price
//
// cache_write_price, when > 0, replaces input_price for the
// prompt_uncached portion (upstream cache-creation discount).
func (cc *CostCalculator) RealCost(mc *model.ModelCost, usage provider.Usage) float64 {
	prompt := float64(usage.PromptTokens)
	completion := float64(usage.CompletionTokens)
	cached := 0.0
	if usage.PromptTokensDetails != nil {
		cached = float64(usage.PromptTokensDetails.CachedTokens)
		if cached > prompt {
			cached = prompt
		}
	}

	inputRate := mc.InputPricePer1M
	if mc.CacheWritePricePer1M > 0 {
		inputRate = mc.CacheWritePricePer1M
	}
	promptUncached := prompt - cached
	cost := tiered(promptUncached, mc.InputPricePer1M,
		mc.InputPricePer1MAbove128K, mc.InputPricePer1MAbove200K, inputRate)
	cost += cached / 1000000.0 * mc.CacheReadPricePer1M

	reasoning := 0.0
	if usage.CompletionTokensDetails != nil {
		reasoning = float64(usage.CompletionTokensDetails.ReasoningTokens)
		if reasoning > completion {
			reasoning = completion
		}
	}
	// Non-reasoning completion billed at output rate (with tier bands).
	cost += tiered(completion-reasoning, mc.OutputPricePer1M,
		mc.OutputPricePer1MAbove128K, mc.OutputPricePer1MAbove200K, mc.OutputPricePer1M)
	if reasoning > 0 {
		rRate := mc.ReasoningPricePer1M
		if rRate <= 0 {
			rRate = mc.OutputPricePer1M
		}
		cost += reasoning / 1000000.0 * rRate
	}

	if mc.ImagePrice > 0 {
		cost += float64(usage.Images) * mc.ImagePrice
	}
	return cost
}

// tiered bills tokens across the long-context bands. baseRate applies
// up to 128k, above128k from 128k..200k, above200k beyond. A zero
// tier rate falls back to the previous band's rate. fallbackRate, when
// set, replaces baseRate (used for cache-write input discount).
func tiered(tokens, baseRate, above128k, above200k, fallbackRate float64) float64 {
	if tokens <= 0 {
		return 0
	}
	b0 := baseRate
	if fallbackRate > 0 {
		b0 = fallbackRate
	}
	r128 := above128k
	if r128 <= 0 {
		r128 = b0
	}
	r200 := above200k
	if r200 <= 0 {
		r200 = r128
	}

	cost := 0.0
	remaining := tokens
	if t128 := float64(model.ModelCostTier128K); remaining > t128 {
		cost += t128 / 1000000.0 * b0
		remaining -= t128
	} else {
		cost += remaining / 1000000.0 * b0
		return cost
	}
	if t200 := float64(model.ModelCostTier200K); remaining > (t200 - float64(model.ModelCostTier128K)) {
		cost += (t200 - float64(model.ModelCostTier128K)) / 1000000.0 * r128
		remaining -= (t200 - float64(model.ModelCostTier128K))
	} else {
		cost += remaining / 1000000.0 * r128
		return cost
	}
	cost += remaining / 1000000.0 * r200
	return cost
}

func (cc *CostCalculator) BilledCost(ctx context.Context, real float64, planMarkupFn func(context.Context) float64) float64 {
	base := real
	if cc.markup != nil {
		base = real * cc.markup()
	}
	if ctx == nil {
		return base
	}
	info, ok := lookupTokenInfo(ctx)
	if !ok || info.PlanMarkupRatio <= 0 {
		return base
	}
	return base * info.PlanMarkupRatio
}
