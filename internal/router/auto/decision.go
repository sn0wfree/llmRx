package auto

// Decision is the full audit trail of one auto-routed request. The
// api layer renders it into the route log line
// (auto_tier=.. cause=.. score=.. arm=.. θ=.. routed=..) and into
// the X-llmRx-Auto-Tier / X-llmRx-Routed-Model response headers.
type Decision struct {
	// Tier is the complexity tier the classifier assigned.
	Tier string `json:"tier"`
	// Score is the classifier's 0..1 complexity score.
	Score float64 `json:"score"`
	// Cause names the classifier that produced the result
	// ("heuristic", "empty"; "llm" and fallbacks land in v1.5).
	Cause string `json:"cause"`
	// Picked is the arm selected by Thompson sampling (or the
	// cheapest candidate while the cold-start gate is on).
	Picked ArmSample `json:"picked"`
	// Candidates are the tier's model list in cost order.
	Candidates []string `json:"candidates"`
	// Attempted lists the models actually tried, in order.
	Attempted []string `json:"attempted"`
	// Routed is the model that produced the 2xx response ("" when
	// every candidate and fallback failed).
	Routed string `json:"routed"`
	// Fallback indicates the winning model came from the combo's
	// Fallback list rather than the tier candidates.
	Fallback bool `json:"fallback"`
}
