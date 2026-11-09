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
		var usedVendor bool
		rctx.Candidates, usedVendor = quotaGuard(rctx.Candidates, e.usage, time.Now())
		if len(rctx.Candidates) < before {
			if usedVendor {
				rctx.LogParts = append(rctx.LogParts, "L2.5(quota,src)")
			} else {
				rctx.LogParts = append(rctx.LogParts, "L2.5(quota)")
			}
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

// VendorUsageProvider is an optional extension of UsageProvider: it
// answers from the vendor's own last report (P22) instead of llmRx's
// local count. Trackers that implement it are preferred by the guard,
// because the vendor knows about traffic that never passed through us.
type VendorUsageProvider interface {
	// SourceRatio returns the vendor-reported consumed fraction for the
	// channel's most constrained live window. ok=false means "no usable
	// vendor data" and the caller falls back to the local ratio.
	SourceRatio(ch *model.Channel, now time.Time) (ratio float64, ok bool)
}

// quotaProvider merges the local count and the vendor report into the
// one provider the guard and the cost router consume. The vendor
// reading wins when it is live: it accounts for traffic that never
// passed through llmRx (the same account shared with other tools).
type quotaProvider struct {
	local  UsageProvider
	vendor VendorUsageProvider
}

// UsageRatio returns the vendor ratio when live, else the local one.
func (q *quotaProvider) UsageRatio(ch *model.Channel, now time.Time) float64 {
	if q.vendor != nil {
		if r, ok := q.vendor.SourceRatio(ch, now); ok {
			return r
		}
	}
	if q.local != nil {
		return q.local.UsageRatio(ch, now)
	}
	return 0
}

// SourceRatio exposes the vendor side so the guard can tell which
// source made the decision.
func (q *quotaProvider) SourceRatio(ch *model.Channel, now time.Time) (float64, bool) {
	if q.vendor == nil {
		return 0, false
	}
	return q.vendor.SourceRatio(ch, now)
}

// quotaGuard compacts out channels whose upstream plan quota window
// is exhausted (usage ratio >= 1). The result is a fresh slice — the
// input may alias the static router's internal index, which must not
// be mutated. Order is preserved (stable compaction); L3 re-sorts
// anyway. Untracked channels always pass.
//
// Vendor-reported usage wins when available: it accounts for traffic
// from other tools sharing the same upstream account. The local count
// is the fallback (and the only data for channels without a probe).
//
// When every candidate is exhausted the input is returned unchanged:
// an upstream throttle response beats a hard routing failure, and the
// admin can see the all-exhausted state on the monitoring page.
func quotaGuard(channels []*model.Channel, up UsageProvider, now time.Time) (kept []*model.Channel, usedVendor bool) {
	vp, hasVendor := up.(VendorUsageProvider)
	kept = make([]*model.Channel, 0, len(channels))
	for _, ch := range channels {
		if !ch.PlanQuota.Tracked() {
			kept = append(kept, ch)
			continue
		}
		ratio, fromVendor := 0.0, false
		if hasVendor {
			ratio, fromVendor = vp.SourceRatio(ch, now)
		}
		if !fromVendor {
			ratio = up.UsageRatio(ch, now)
		}
		if ratio >= 1 {
			// The channel is being dropped. Remember whether the
			// decision came from the vendor so the log line can say so
			// — that is the diagnostic an operator needs when a channel
			// disappears from rotation with a low local count.
			usedVendor = usedVendor || fromVendor
			continue
		}
		kept = append(kept, ch)
	}
	if len(kept) == 0 {
		return channels, usedVendor
	}
	return kept, usedVendor
}
