package api

import (
	"context"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/router"
	"net/http"
)

// modelCostFor resolves a model's rate card from the in-memory
// cache. ok=false when unpriced (requests for it are rejected
// fail-closed before the upstream call).
func (h *Handler) modelCostFor(model string) (*model.ModelCost, bool) {
	if h.modelCosts == nil {
		return nil, false
	}
	mc, ok := h.modelCosts.Lookup(model)
	if !ok {
		return nil, false
	}
	return &mc, true
}

// requirePriced writes a 422 and returns false when any of the given
// models is absent from the rate card (fail-closed: unpriced models
// must never run upstream for free). An empty rate card is treated as
// "no pricing configured yet" (fresh DB before startup seeding, or a
// test harness) and allows requests — production always seeds the
// card at boot, so fail-closed applies as soon as it is non-empty.
func (h *Handler) requirePriced(w http.ResponseWriter, models ...string) bool {
	if h.modelCosts == nil || h.modelCosts.Size() == 0 {
		return true
	}
	for _, m := range models {
		if m == "" {
			continue
		}
		if _, ok := h.modelCostFor(m); !ok {
			writeError(w, http.StatusUnprocessableEntity,
				"model "+m+" is not priced; ask the operator to add it to the rate card",
				"model_not_priced")
			return false
		}
	}
	return true
}

func (h *Handler) emitLog(ctx context.Context, tokenID int64, modelName string, route *router.RouteResult, usage *provider.Usage, durationMs int64, statusCode int, failed bool, ip string) {
	h.emitter.Emit(ctx, tokenID, modelName, route, usage, durationMs, statusCode, failed, ip)
}

// billedCost returns the per-token / per-plan-adjusted billed cost.
// Server-wide markup is applied first; if the token has a Plan with
// a non-1.0 markup_ratio, it scales on top. The per-plan markup is
// cached in TokenInfo.PlanMarkupRatio (populated by
// tokencache.Reload), so the hot path is allocation-free.
func (h *Handler) billedCost(ctx context.Context, real float64) float64 {
	base := real * h.Markup()
	if ctx == nil {
		return base
	}
	info, ok := lookupTokenInfo(ctx)
	if !ok || info.PlanMarkupRatio <= 0 {
		return base
	}
	return base * info.PlanMarkupRatio
}
