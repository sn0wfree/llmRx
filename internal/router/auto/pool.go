package auto

import (
	"strings"

	"github.com/sn0wfree/llmRx/internal/router/thompson"
)

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
