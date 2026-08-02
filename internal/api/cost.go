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

func (cc *CostCalculator) RealCost(ch *model.Channel, usage provider.Usage) float64 {
	prompt := float64(usage.PromptTokens)
	cached := 0.0
	if usage.PromptTokensDetails != nil {
		cached = float64(usage.PromptTokensDetails.CachedTokens)
		if cached > prompt {
			cached = prompt
		}
	}
	normal := (prompt - cached) / 1000000.0 * ch.InputPrice
	cachedCost := 0.0
	if ch.CachedInputDiscount > 0 {
		cachedCost = cached / 1000000.0 * ch.InputPrice * ch.CachedInputDiscount
	}
	output := float64(usage.CompletionTokens) / 1000000.0 * ch.OutputPrice
	return normal + cachedCost + output
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
