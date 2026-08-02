package router

import (
	"context"
	"time"

	"github.com/sn0wfree/llmRx/internal/intent"
	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/router/thompson"
)

type RouteContext struct {
	ModelName  string
	Options    RouteOptions
	Candidates []*model.Channel
	LogParts   []string
	Intent     intent.Intent
	Error      error
}

type RoutingStage interface {
	Name() string
	Apply(ctx context.Context, rctx *RouteContext)
}

func (e *RouterEngine) buildPipeline() []RoutingStage {
	return []RoutingStage{
		&staticStage{static: e.static, extraChannels: e.extraChannels},
		&breakerStage{breaker: e.breaker},
		&costStage{cost: e.cost},
		&intentStage{intent: e.intent},
		&thompsonStage{sampler: e.thompson},
	}
}

func (e *RouterEngine) routeWithPipeline(ctx context.Context, modelName string, opts RouteOptions) (*RouteResult, error) {
	start := time.Now()
	rctx := &RouteContext{
		ModelName: modelName,
		Options:   opts,
	}

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

	for _, stage := range e.buildPipeline() {
		stage.Apply(ctx, rctx)
		if rctx.Error != nil {
			return nil, rctx.Error
		}
		if len(rctx.Candidates) == 0 {
			if stage.Name() == "breaker" {
				return nil, ErrAllBroken
			}
			return nil, ErrNoChannel
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

func joinLog(parts []string) string {
	s := ""
	for i, p := range parts {
		if i > 0 {
			s += " → "
		}
		s += p
	}
	return s
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

type staticStage struct {
	static        *StaticRouter
	extraChannels []func() []*model.Channel
}

func (s *staticStage) Name() string                                { return "static" }
func (s *staticStage) Apply(_ context.Context, rctx *RouteContext) {}

type breakerStage struct {
	breaker *CircuitBreaker
}

func (s *breakerStage) Name() string { return "breaker" }
func (s *breakerStage) Apply(_ context.Context, rctx *RouteContext) {
	if rctx.Options.SkipBreaker {
		// Safety net: the auto router's fallback attempts go through
		// even when every candidate channel is open — a degraded
		// call beats a hard failure.
		rctx.LogParts = append(rctx.LogParts, "L2(breaker,bypass)")
		return
	}
	rctx.Candidates = s.breaker.Filter(rctx.Candidates)
	rctx.LogParts = append(rctx.LogParts, "L2(breaker)")
	if len(rctx.Candidates) == 0 {
		rctx.Error = ErrAllBroken
	}
}

type costStage struct {
	cost *CostRouter
}

func (s *costStage) Name() string { return "cost" }
func (s *costStage) Apply(_ context.Context, rctx *RouteContext) {
	if rctx.Options.CostStrategy != "" {
		rctx.Candidates = s.cost.SortWith(rctx.Candidates, rctx.Options.CostStrategy)
		rctx.LogParts = append(rctx.LogParts, "L3(cost="+string(rctx.Options.CostStrategy)+")")
	} else {
		rctx.Candidates = s.cost.Sort(rctx.Candidates)
		rctx.LogParts = append(rctx.LogParts, "L3(cost)")
	}
}

type intentStage struct {
	intent intent.Classifier
}

func (s *intentStage) Name() string { return "intent" }
func (s *intentStage) Apply(_ context.Context, rctx *RouteContext) {
	if rctx.Options.Text == "" || s.intent == nil {
		return
	}
	intn := s.intent.Classify(rctx.Options.Text)
	rctx.Intent = intn
	if intn.Kind == "unknown" || intn.Kind == "general" || len(rctx.Candidates) <= 1 {
		return
	}
	n := splitByIntent(rctx.Candidates, intn.Kind)
	if n > 0 {
		// In-place partition: matched channels already occupy
		// Candidates[:n], so the ordering is already correct.
		rctx.LogParts = append(rctx.LogParts, "L4(intent="+intn.Kind+")")
	}
}

type thompsonStage struct {
	sampler *thompson.Sampler
}

func (s *thompsonStage) Name() string { return "thompson" }
func (s *thompsonStage) Apply(_ context.Context, rctx *RouteContext) {
	if len(rctx.Candidates) <= 1 {
		return
	}
	ranked := s.sampler.Sample(rctx.Candidates)
	rctx.LogParts = append(rctx.LogParts, "L5(thompson)")
	rctx.Candidates = nil
	for _, r := range ranked {
		rctx.Candidates = append(rctx.Candidates, r.Channel)
	}
}
