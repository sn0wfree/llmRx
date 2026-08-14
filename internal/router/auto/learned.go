package auto

import (
	"sync"
)

// LearnedRouter predicts the best model for a given prompt using a
// trained ML model (offline-trained, loaded at startup). The
// interface is intentionally simple so Phase 1 can use a mock and
// Phase 2 can swap in ONNX inference without changing callers.
//
// All methods are safe for concurrent use.
type LearnedRouter interface {
	// Infer returns a score per candidate model (higher = better
	// fit for this prompt). Scores are in [0, 1]; the caller
	// blends them with TS bandit and cost weights.
	// needs is used for capability-aware scoring (e.g. penalize
	// models that don't support tools when the request needs them).
	Infer(prompt string, candidates []string, needs Needs) map[string]float64

	// Reload re-reads the model file from disk. Returns nil if
	// no model is configured (nop behavior).
	Reload() error

	// Loaded reports whether a trained model is available.
	Loaded() bool
}

// Needs captures capability signals extracted from the request.
// Used by the learned router to penalize candidates that lack
// required capabilities.
type Needs struct {
	Tools    bool // request has tools/functions
	Vision   bool // request has image_url parts
	JSONMode bool // request has response_format=json_object
	Reasoning bool // request has reasoning_effort or thinking
}

// IsEmpty reports whether no capability is required.
func (n Needs) IsEmpty() bool {
	return !n.Tools && !n.Vision && !n.JSONMode && !n.Reasoning
}

// nopLearnedRouter is the default when no model file is configured
// or loaded. All scores are zero, meaning the learned prior has no
// influence on the fusion formula.
type nopLearnedRouter struct{}

func (nopLearnedRouter) Infer(string, []string, Needs) map[string]float64 { return nil }
func (nopLearnedRouter) Reload() error                                    { return nil }
func (nopLearnedRouter) Loaded() bool                                     { return false }

// MockLearnedRouter is a configurable mock for testing. It returns
// fixed scores or uniform distribution depending on configuration.
// Exported so tests in other packages can use it.
type MockLearnedRouter struct {
	mu      sync.RWMutex
	loaded  bool
	scores  map[string]float64 // fixed per-model scores (model -> score)
	uniform float64            // if > 0, return this for all candidates
}

// NewMockLearnedRouter returns a mock that returns uniform scores.
func NewMockLearnedRouter() *MockLearnedRouter {
	return &MockLearnedRouter{loaded: true, uniform: 0.5}
}

// NewMockLearnedRouterWithScores returns a mock with fixed per-model scores.
func NewMockLearnedRouterWithScores(scores map[string]float64) *MockLearnedRouter {
	return &MockLearnedRouter{loaded: true, scores: scores}
}

func (m *MockLearnedRouter) Infer(_ string, candidates []string, _ Needs) map[string]float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.loaded {
		return nil
	}
	out := make(map[string]float64, len(candidates))
	if m.uniform > 0 {
		for _, c := range candidates {
			out[c] = m.uniform
		}
		return out
	}
	for _, c := range candidates {
		if s, ok := m.scores[c]; ok {
			out[c] = s
		} else {
			out[c] = 0.5 // unknown model → neutral
		}
	}
	return out
}

func (m *MockLearnedRouter) Reload() error { return nil }

func (m *MockLearnedRouter) Loaded() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.loaded
}

// SetLoaded toggles the loaded flag (for testing hot-reload paths).
func (m *MockLearnedRouter) SetLoaded(v bool) {
	m.mu.Lock()
	m.loaded = v
	m.mu.Unlock()
}
