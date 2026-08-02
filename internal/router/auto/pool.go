package auto

import (
	"strings"

	"github.com/sn0wfree/llmRx/internal/modelmeta"
	"github.com/sn0wfree/llmRx/internal/router/thompson"
)

// contextHeadroom is the fraction of the estimated token count
// reserved for the response (and tokenizer error): a model is only
// eligible when contextWindow >= estimate * (1 + contextHeadroom).
const contextHeadroom = 0.2

// ArmKey builds the Thompson arm key for a (tier, model) pair.
// The key format is "tier:model", e.g. "simple:deepseek-chat".
// The thompson package treats keys as opaque strings, so the
// format is safe to change as long as both writers and readers
// use this helper.
func ArmKey(tier, model string) string { return tier + ":" + model }

// ParseArmKey splits an arm key back into (tier, model).
func ParseArmKey(key string) (tier, model string) {
	i := strings.IndexByte(key, ':')
	if i < 0 {
		return "", key
	}
	return key[:i], key[i+1:]
}

// ArmSample is the outcome of sampling the candidate arms of one
// tier: the model whose arm drew the highest θ, plus the θ value
// for the decision log.
type ArmSample struct {
	Model string  // selected model ("" if no candidates)
	Key   string  // arm key "tier:model"
	Score float64 // θ draw; 0 when the cold-start gate is on
}

// Pool wraps the Thompson sampler for auto-router model
// selection. It is cheap to construct and safe for concurrent
// use (the sampler carries its own lock).
type Pool struct {
	sampler *thompson.Sampler
}

// NewPool returns a Pool over the given sampler. Pass the engine's
// shared sampler so arms persist with the L5 state file.
func NewPool(s *thompson.Sampler) *Pool {
	return &Pool{sampler: s}
}

// FilterContextCandidates drops candidates whose documented context
// window cannot fit the estimated prompt tokens (TokenEstimate plus
// headroom). Models with no modelmeta entry are kept — the filter
// is lenient, never a hard gate on unknowns. When every candidate
// would be dropped the input list is returned unchanged: starving
// the tier is worse than risking an upstream rejection.
func FilterContextCandidates(candidates []string, estimatedTokens int) []string {
	if len(candidates) <= 1 || estimatedTokens <= 0 {
		return candidates
	}
	need := estimatedTokens + int(float64(estimatedTokens)*contextHeadroom)
	out := make([]string, 0, len(candidates))
	for _, m := range candidates {
		meta, ok := modelmeta.Get(m)
		if !ok || meta.ContextWindow <= 0 || meta.ContextWindow >= need {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return candidates
	}
	return out
}

// Select samples the candidate arms of one tier and returns the
// model with the highest θ draw. candidates must be in cost order
// (cheapest first): when the cold-start gate is on (any arm below
// the sampler's minimum observation count) SampleArms returns the
// input order unchanged, so Select picks the cheapest candidate —
// the same "let static cost order collect baseline data" semantics
// as L5's channel sampling.
func (p *Pool) Select(tier string, candidates []string) ArmSample {
	if len(candidates) == 0 {
		return ArmSample{}
	}
	keys := make([]string, len(candidates))
	for i, m := range candidates {
		keys[i] = ArmKey(tier, m)
	}
	ranked := p.sampler.SampleArms(keys)
	top := ranked[0]
	model := ""
	for i, k := range keys {
		if k == top.Arm {
			model = candidates[i]
			break
		}
	}
	return ArmSample{Model: model, Key: top.Arm, Score: top.Score}
}
