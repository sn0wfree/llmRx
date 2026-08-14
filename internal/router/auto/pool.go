package auto

import (
	"sort"
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

// AllContextFiltered reports whether every candidate has a
// documented context window too small for the estimated prompt
// tokens. Unknown models (no modelmeta entry) count as "fits" —
// the filter is lenient. Used by the api layer to decide whether
// to escalate to the combo's context_fallback list.
func AllContextFiltered(candidates []string, estimatedTokens int) bool {
	if len(candidates) == 0 || estimatedTokens <= 0 {
		return false
	}
	need := estimatedTokens + int(float64(estimatedTokens)*contextHeadroom)
	for _, m := range candidates {
		meta, ok := modelmeta.Get(m)
		if !ok || meta.ContextWindow <= 0 || meta.ContextWindow >= need {
			return false
		}
	}
	return true
}

// SelectContextFallback picks from the combo's context_fallback
// list the models whose documented context window can fit the
// estimated prompt tokens, sorted by context_window ascending so
// the smallest (usually cheapest) fitting model is tried first.
// Returns the original list order when no modelmeta entry exists
// or no model fits (the caller will try them anyway — degraded
// attempt beats a hard failure).
func SelectContextFallback(candidates []string, estimatedTokens int) []string {
	if len(candidates) <= 1 || estimatedTokens <= 0 {
		return candidates
	}
	need := estimatedTokens + int(float64(estimatedTokens)*contextHeadroom)
	type candidate struct {
		model string
		meta  *modelmeta.ModelMeta
	}
	var fitting []candidate
	for _, m := range candidates {
		meta, ok := modelmeta.Get(m)
		if !ok || meta.ContextWindow <= 0 {
			// Unknown model: keep it, ordered by its slot in the
			// list (can't rank by context window without data).
			fitting = append(fitting, candidate{model: m})
			continue
		}
		if meta.ContextWindow >= need {
			fitting = append(fitting, candidate{model: m, meta: meta})
		}
	}
	if len(fitting) == 0 {
		return candidates
	}
	sort.SliceStable(fitting, func(i, j int) bool {
		ci, cj := fitting[i].meta, fitting[j].meta
		if ci == nil || ci.ContextWindow <= 0 {
			return false
		}
		if cj == nil || cj.ContextWindow <= 0 {
			return true
		}
		return ci.ContextWindow < cj.ContextWindow
	})
	out := make([]string, len(fitting))
	for i, c := range fitting {
		out[i] = c.model
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
