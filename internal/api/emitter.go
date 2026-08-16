package api

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/sn0wfree/llmRx/internal/broker"
	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/mcp"
	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/modelcost"
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
	costs     *modelcost.Cache

	// chatLogCount is a monotonic counter used by the chat.completed
	// sampling hook. Accessed via sync/atomic.AddUint64; padded to
	// its own cache line (chatLogSampleRate sits adjacent) so the
	// counter hot-path doesn't share a line with the rate field.
	chatLogCount      uint64
	chatLogSampleRate uint64 // 1 = every; 0 = disabled; N = 1-in-N
}

func NewLogEmitter(ls *logstore.Manager, lb *broker.Broker[*model.Log], st store.Store, lim *ratelimit.Limiter, cc *CostCalculator, costs *modelcost.Cache, sampleRate int) *LogEmitter {
	rate := uint64(sampleRate)
	if rate > 1 {
		// Valid N>1: keep as-is.
	} else if rate == 1 {
		// Default: every request.
	} else {
		// 0 or negative: disable the chat.completed line entirely.
		rate = 0
	}
	return &LogEmitter{
		logStore:          ls,
		logBroker:         lb,
		store:             st,
		limits:            lim,
		costCalc:          cc,
		costs:             costs,
		chatLogSampleRate: rate,
	}
}

// SetChatLogSampleRate atomically swaps the sampling rate. Pass 1
// to log every request, 0 to disable, or N>=2 to log 1-in-N.
// The change is picked up by the next Emit call. Hot path skips
// the atomic when rate is 1 (the default).
func (e *LogEmitter) SetChatLogSampleRate(n int) {
	var rate uint64
	switch {
	case n > 1:
		rate = uint64(n)
	case n == 1:
		rate = 1
	default:
		rate = 0
	}
	atomic.StoreUint64(&e.chatLogSampleRate, rate)
	// Reset the counter so the next sampled call still emits at the
	// start of a new window instead of waiting for the next %N==0.
	atomic.StoreUint64(&e.chatLogCount, 0)
}

// emitChatCompletedSamples decides whether to emit the chat.completed
// Info log line for this request. Returns true when the line should
// be emitted. The persisted logstore/broker/metrics/spend paths run
// for every request regardless of this decision.
func (e *LogEmitter) emitChatCompletedSamples() bool {
	rate := atomic.LoadUint64(&e.chatLogSampleRate)
	if rate == 1 {
		return true
	}
	if rate == 0 {
		return false
	}
	n := atomic.AddUint64(&e.chatLogCount, 1)
	return n%rate == 0
}

func (e *LogEmitter) Emit(ctx context.Context, tokenID int64, modelName string, route *router.RouteResult, usage *provider.Usage, durationMs int64, statusCode int, failed bool, ip string) {
	real := 0.0
	cached := 0
	if usage != nil {
		if images := imagesFromContext(ctx); images > 0 {
			usage.Images = images
		}
		// Resolve the model's rate card. The request is fail-closed
		// against unpriced models before reaching upstream, so a
		// missing card here is anomalous — bill nothing rather than
		// fabricate a price.
		if mc, ok := e.costFor(modelName); ok {
			real = e.costCalc.RealCost(mc, *usage)
		}
		if usage.PromptTokensDetails != nil {
			cached = usage.PromptTokensDetails.CachedTokens
		}
	}
	billed := e.costCalc.BilledCost(ctx, real, func(_ context.Context) float64 { return 1.0 })
	status := "ok"
	if failed {
		status = "fail"
	}
	if e.emitChatCompletedSamples() {
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
			logging.F("sample_rate", atomic.LoadUint64(&e.chatLogSampleRate)),
		)
	}
	entry := &model.Log{
		TokenID:          tokenID,
		ChannelID:        route.Channel.ID,
		KeyID:            route.Key.ID,
		Model:            modelName,
		PromptTokens:     promptTokens(usage),
		CompletionTokens: completionTokens(usage),
		CachedTokens:     cached,
		ReasoningTokens:  reasoningTokens(usage),
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
	// Track per-channel cumulative spend for fast dashboard queries.
	if e.store != nil && route.Channel.ID > 0 && real > 0 {
		if err := e.store.RecordChannelSpend(route.Channel.ID, real); err != nil {
			logging.Warn("record channel spend failed", logging.F("error", err.Error()))
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
func reasoningTokens(u *provider.Usage) int {
	if u == nil || u.CompletionTokensDetails == nil {
		return 0
	}
	return u.CompletionTokensDetails.ReasoningTokens
}

// costFor resolves a model's rate card. Returns ok=false when the
// model is unpriced.
func (e *LogEmitter) costFor(modelName string) (*model.ModelCost, bool) {
	if e.costs == nil {
		return nil, false
	}
	mc, ok := e.costs.Lookup(modelName)
	if !ok {
		return nil, false
	}
	return &mc, true
}

// EmitCacheHit records a response-cache hit: no upstream call, so
// the stored request usage is reused with all prompt tokens treated
// as cached (billed at the model's cache_read rate) plus completion
// at the output rate. Cache hits count toward usage statistics and
// spend exactly like normal requests.
func (e *LogEmitter) EmitCacheHit(ctx context.Context, tokenID int64, modelName string, usage *provider.Usage, ip string) {
	if usage == nil {
		return
	}
	real := 0.0
	if mc, ok := e.costFor(modelName); ok {
		// Treat the whole prompt as cache-served.
		u := *usage
		u.PromptTokensDetails = &provider.PromptTokensDetails{CachedTokens: u.PromptTokens}
		real = e.costCalc.RealCost(mc, u)
	}
	billed := e.costCalc.BilledCost(ctx, real, func(_ context.Context) float64 { return 1.0 })
	entry := &model.Log{
		TokenID:          tokenID,
		Model:            modelName,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		CachedTokens:     usage.PromptTokens,
		ReasoningTokens:  reasoningTokens(usage),
		RealCostUSD:      real,
		BilledCostUSD:    billed,
		StatusCode:       200,
		RouterPath:       "cache",
		RequestIP:        ip,
		CacheHit:         true,
	}
	if e.logStore != nil {
		if err := e.logStore.Insert(entry); err != nil {
			logging.Warn("persist cache-hit log failed", logging.F("error", err.Error()))
		}
	}
	if e.logBroker != nil {
		e.logBroker.Publish(entry)
	}
	observability.RecordRequest(modelName, 0, false, billed, usage.PromptTokens, usage.CompletionTokens, false)
	planID := planIDFromContext(ctx)
	if tokenID > 0 && billed > 0 && e.store != nil {
		if err := e.store.RecordRequestSpend(tokenID, planID, billed); err != nil {
			if errors.Is(err, store.ErrBudgetExceeded) {
				logging.Warn("plan budget exceeded for cache hit, token ledger untouched",
					logging.F("plan_id", planID),
					logging.F("billed_usd", billed),
				)
			} else {
				logging.Warn("record cache-hit spend failed", logging.F("error", err.Error()))
			}
		}
	}
	if tokenID > 0 && e.limits != nil {
		e.limits.Account(tokenID, usage.PromptTokens+usage.CompletionTokens)
	}
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
