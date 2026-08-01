package api

import (
	"context"
	"errors"

	"github.com/sn0wfree/llmRx/internal/broker"
	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/mcp"
	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/observability"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/ratelimit"
	"github.com/sn0wfree/llmRx/internal/router"
	"github.com/sn0wfree/llmRx/internal/store"
)

type LogEmitter struct {
	logStore  *logstore.Manager
	logBroker *broker.Broker[*model.Log]
	store     store.Store
	limits    *ratelimit.Limiter
	costCalc  *CostCalculator
}

func NewLogEmitter(ls *logstore.Manager, lb *broker.Broker[*model.Log], st store.Store, lim *ratelimit.Limiter, cc *CostCalculator) *LogEmitter {
	return &LogEmitter{
		logStore:  ls,
		logBroker: lb,
		store:     st,
		limits:    lim,
		costCalc:  cc,
	}
}

func (e *LogEmitter) Emit(ctx context.Context, tokenID int64, modelName string, route *router.RouteResult, usage *provider.Usage, durationMs int64, statusCode int, failed bool, ip string) {
	real := 0.0
	cached := 0
	if usage != nil {
		real = e.costCalc.RealCost(route.Channel, *usage)
		if usage.PromptTokensDetails != nil {
			cached = usage.PromptTokensDetails.CachedTokens
		}
	}
	billed := e.costCalc.BilledCost(ctx, real, func(_ context.Context) float64 { return 1.0 })
	status := "ok"
	if failed {
		status = "fail"
	}
	logging.Info("chat.completed",
		logging.F("status", status),
		logging.F("model", modelName),
		logging.F("channel", route.Channel.Name),
		logging.F("key", route.Key.KeyMasked),
		logging.F("prompt", promptTokens(usage)),
		logging.F("completion", completionTokens(usage)),
		logging.F("cached", cached),
		logging.F("real_usd", real),
		logging.F("billed_usd", billed),
		logging.F("duration_ms", durationMs),
		logging.F("code", statusCode),
		logging.F("path", route.RouterLog),
	)
	entry := &model.Log{
		TokenID:          tokenID,
		ChannelID:        route.Channel.ID,
		KeyID:            route.Key.ID,
		Model:            modelName,
		PromptTokens:     promptTokens(usage),
		CompletionTokens: completionTokens(usage),
		CachedTokens:     cached,
		RealCostUSD:      real,
		BilledCostUSD:    billed,
		DurationMs:       durationMs,
		StatusCode:       statusCode,
		RouterPath:       route.RouterLog,
		RequestIP:        ip,
	}
	if e.logStore != nil {
		if err := e.logStore.Insert(entry); err != nil {
			logging.Warn("persist log failed", logging.F("error", err.Error()))
		}
	}
	if e.logBroker != nil {
		e.logBroker.Publish(entry)
	}
	observability.RecordRequest(modelName, durationMs, failed, billed,
		promptTokens(usage), completionTokens(usage), false)
	planID := planIDFromContext(ctx)
	if tokenID > 0 && billed > 0 && e.store != nil {
		if err := e.store.RecordRequestSpend(tokenID, planID, billed); err != nil {
			if errors.Is(err, store.ErrBudgetExceeded) {
				logging.Warn("plan budget exceeded, token ledger untouched",
					logging.F("plan_id", planID),
					logging.F("billed_usd", billed),
				)
			} else {
				logging.Warn("record request spend failed", logging.F("error", err.Error()))
			}
		}
	}
	if tokenID > 0 && usage != nil && e.limits != nil {
		e.limits.Account(tokenID, usage.PromptTokens+usage.CompletionTokens)
	}
}

// EmitMCP records one log row per executed MCP tool call. MCP calls
// count toward RPM (once per call) but not TPM, since they do not
// consume LLM tokens. Billed cost uses the per-tool rate card; the
// plan-spend ledger is gated by the same ErrBudgetExceeded logic as
// regular chat requests.
func (e *LogEmitter) EmitMCP(ctx context.Context, tokenID int64, route *router.RouteResult, usage mcp.MCPUsage, ip string) {
	if len(usage.Calls) == 0 {
		return
	}
	planID := planIDFromContext(ctx)
	for _, call := range usage.Calls {
		modelName := call.ServerName
		if modelName == "" {
			modelName = call.Name
		}
		billed := e.costCalc.BilledCost(ctx, call.CostUSD, func(_ context.Context) float64 { return 1.0 })
		logging.Info("mcp.tool.completed",
			logging.F("tool", call.Name),
			logging.F("server", call.ServerName),
			logging.F("real_usd", call.CostUSD),
			logging.F("billed_usd", billed),
		)
		entry := &model.Log{
			TokenID:       tokenID,
			ChannelID:     route.Channel.ID,
			KeyID:         route.Key.ID,
			Model:         modelName,
			RealCostUSD:   call.CostUSD,
			BilledCostUSD: billed,
			StatusCode:    200,
			RouterPath:    route.RouterLog,
			RequestIP:     ip,
			Endpoint:      "mcp",
			Units:         1,
		}
		if e.logStore != nil {
			if err := e.logStore.Insert(entry); err != nil {
				logging.Warn("persist mcp log failed", logging.F("error", err.Error()))
			}
		}
		if e.logBroker != nil {
			e.logBroker.Publish(entry)
		}
		observability.RecordRequest("mcp", 0, false, billed, 0, 0, false)
		if tokenID > 0 && billed > 0 && e.store != nil {
			if err := e.store.RecordRequestSpend(tokenID, planID, billed); err != nil {
				if errors.Is(err, store.ErrBudgetExceeded) {
					logging.Warn("plan budget exceeded for mcp call, token ledger untouched",
						logging.F("plan_id", planID),
						logging.F("billed_usd", billed),
					)
				} else {
					logging.Warn("record mcp spend failed", logging.F("error", err.Error()))
				}
			}
		}
		if tokenID > 0 && e.limits != nil {
			e.limits.AccountRequest(tokenID)
		}
	}
}

func promptTokens(u *provider.Usage) int {
	if u == nil {
		return 0
	}
	return u.PromptTokens
}
func completionTokens(u *provider.Usage) int {
	if u == nil {
		return 0
	}
	return u.CompletionTokens
}

func planIDFromContext(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	v, ok := ctx.Value(middleware.TokenInfoKey).(middleware.TokenInfo)
	if !ok {
		return 0
	}
	return v.PlanID
}