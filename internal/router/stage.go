package router

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/intent"
	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/model"
)

type RouteContext struct {
	ModelName  string
	Options    RouteOptions
	Candidates []*model.Channel
	LogParts   []string
	Intent     intent.Intent
	Error      error
}

var rctxPool = sync.Pool{
	New: func() any { return &RouteContext{} },
}

func (e *RouterEngine) routeWithPipeline(ctx context.Context, modelName string, opts RouteOptions) (*RouteResult, error) {
	start := time.Now()
	rctx := rctxPool.Get().(*RouteContext)
	*rctx = RouteContext{ModelName: modelName, Options: opts}

	defer func() {
		rctx.Candidates = nil
		rctx.LogParts = nil
		rctxPool.Put(rctx)
	}()

	// L1: static match
	if len(opts.ModelSet) > 0 {
		rctx.Candidates = e.static.MatchAny(opts.ModelSet)
		rctx.LogParts = append(rctx.LogParts, "L1(static,combo)")
	} else {
		rctx.Candidates = e.static.Match(modelName)
		rctx.LogParts = append(rctx.LogParts, "L1(static)")
	}
	for _, src := range e.extraChannels {
		rctx.Candidates = append(rctx.Candidates, src()...)
	}
	if len(rctx.Candidates) == 0 {
		return nil, ErrNoChannel
	}

	// L2: breaker
	if opts.SkipBreaker {
		rctx.LogParts = append(rctx.LogParts, "L2(breaker,bypass)")
	} else {
		rctx.Candidates = e.breaker.Filter(rctx.Candidates)
		rctx.LogParts = append(rctx.LogParts, "L2(breaker)")
		if len(rctx.Candidates) == 0 {
			return nil, ErrAllBroken
		}
	}

	// L2.5: quota guard (P21). Channels whose upstream plan quota
	// window is exhausted are dropped — unless every candidate is
	// exhausted, where serving the request anyway beats failing it
	// (quotaGuard returns the input unchanged in that case).
	if e.usage != nil && (e.quotaGuardOn == nil || e.quotaGuardOn()) {
		before := len(rctx.Candidates)
		rctx.Candidates = quotaGuard(rctx.Candidates, e.usage, time.Now())
		if len(rctx.Candidates) < before {
			rctx.LogParts = append(rctx.LogParts, "L2.5(quota)")
		}
	}

	// L3: cost
	if opts.CostStrategy != "" {
		rctx.Candidates = e.cost.SortWith(rctx.Candidates, opts.CostStrategy)
		rctx.LogParts = append(rctx.LogParts, "L3(cost="+string(opts.CostStrategy)+")")
	} else {
		rctx.Candidates = e.cost.Sort(rctx.Candidates)
		rctx.LogParts = append(rctx.LogParts, "L3(cost)")
	}

	// L4: intent
	if opts.Text != "" && e.intent != nil {
		if len(rctx.Candidates) > 1 {
			intn := e.intent.Classify(opts.Text)
			rctx.Intent = intn
			if intn.Kind != "unknown" && intn.Kind != "general" {
				n := splitByIntent(rctx.Candidates, intn.Kind)
				if n > 0 {
					rctx.LogParts = append(rctx.LogParts, "L4(intent="+intn.Kind+")")
				}
			}
		}
	}

	// L5: thompson
	if len(rctx.Candidates) > 1 {
		ranked := e.thompson.Sample(rctx.Candidates)
		rctx.LogParts = append(rctx.LogParts, "L5(thompson)")
		rctx.Candidates = nil
		for _, r := range ranked {
			rctx.Candidates = append(rctx.Candidates, r.Channel)
		}
	}

	ch := rctx.Candidates[0]
	rctx.LogParts = append(rctx.LogParts, "select="+ch.Name)

	key, err := e.pool.NextKey(ch.ID)
	if err != nil {
		return nil, err
	}

	result := &RouteResult{
		Channel:   ch,
		Key:       key,
		KeyValue:  key.Key,
		RouterLog: joinLog(rctx.LogParts),
		Intent:    rctx.Intent,
	}

	logging.Debug("route",
		logging.F("model", modelName),
		logging.F("channel", ch.Name),
		logging.F("path", joinLog(rctx.LogParts)),
		logging.F("duration_ms", time.Since(start).Milliseconds()),
	)
	return result, nil
}

// joinLog concatenates parts into a single "a → b → c" form. The
// previous implementation used repeated `+=` which allocates an
// intermediate string per append; for 5 parts that's 4 alloc +
// 4 copies. strings.Builder follows the standard pre-size pattern
// (sum of len(part) + 3*(n-1) separator bytes) so the final
// String() never grows.
func joinLog(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	const sep = " → "
	n := len(sep) * (len(parts) - 1)
	for _, p := range parts {
		n += len(p)
	}
	var sb strings.Builder
	sb.Grow(n)
	sb.WriteString(parts[0])
	for i := 1; i < len(parts); i++ {
		sb.WriteString(sep)
		sb.WriteString(parts[i])
	}
	return sb.String()
}

// splitByIntent partitions channels in place so that channels whose
// Intents list contains kind occupy channels[:n] and the rest fill
// channels[n:]. Returns the boundary n.
//
// NOT stable: the swap-based partition preserves the matched
// group's relative order, but the non-matched group can be
// reordered. Fine for L5 (channel priority is recomputed by the
// sampler, not by slice position); documented because the original
// comment claimed a stable partition.
func splitByIntent(channels []*model.Channel, kind string) int {
	// Two-pointer in-place stable partition. We walk the slice
	// looking for matches; when we find one, we slide it forward
	// into the matched region via adjacent swaps.
	matched := 0
	for i := 0; i < len(channels); i++ {
		if containsString(channels[i].Intents, kind) {
			if i != matched {
				channels[i], channels[matched] = channels[matched], channels[i]
			}
			matched++
		}
	}
	return matched
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// quotaGuard compacts out channels whose upstream plan quota window
// is exhausted (usage ratio >= 1). The result is a fresh slice — the
// input may alias the static router's internal index, which must not
// be mutated. Order is preserved (stable compaction); L3 re-sorts
// anyway. Untracked channels always pass.
//
// When every candidate is exhausted the input is returned unchanged:
// an upstream throttle response beats a hard routing failure, and the
// admin can see the all-exhausted state on the monitoring page.
func quotaGuard(channels []*model.Channel, up UsageProvider, now time.Time) []*model.Channel {
	kept := make([]*model.Channel, 0, len(channels))
	for _, ch := range channels {
		if !ch.PlanQuota.Tracked() || up.UsageRatio(ch, now) < 1 {
			kept = append(kept, ch)
		}
	}
	if len(kept) == 0 {
		return channels
	}
	return kept
}
