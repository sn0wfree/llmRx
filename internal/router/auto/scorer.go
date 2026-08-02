// Package auto implements the auto-routing tier: model
// auto-selection (complexity classification + arm sampling) and
// channel auto-scheduling for mode:auto combos.
package auto

import (
	"math"
	"regexp"
	"strings"
)

// Tier is a complexity tier that maps a request to a candidate
// model table. Higher tiers allow more capable (and costlier)
// models.
type Tier string

// The four complexity tiers, ordered from cheapest to most
// capable.
const (
	TierSimple   Tier = "simple"
	TierStandard Tier = "standard"
	TierComplex  Tier = "complex"
	TierAgentic  Tier = "agentic"
)

// AllTiers lists the tiers in ascending cost order.
var AllTiers = []Tier{TierSimple, TierStandard, TierComplex, TierAgentic}

// ValidTier reports whether t is one of the known tiers.
func ValidTier(t string) bool {
	for _, k := range AllTiers {
		if string(k) == t {
			return true
		}
	}
	return false
}

// Thresholds maps a 0..1 complexity score to a tier. They are the
// configurable defaults (global, runtime-adjustable in commit 3).
type Thresholds [3]float64

// DefaultThresholds are the shipped score cutoffs:
//
//	simple    score < 0.25
//	standard  0.25 <= score < 0.55
//	complex   0.55 <= score < 0.80
//	agentic   score >= 0.80
func DefaultThresholds() Thresholds { return Thresholds{0.25, 0.55, 0.80} }

// MapTier maps a score to a tier using the given thresholds.
func MapTier(score float64, th Thresholds) Tier {
	switch {
	case score >= th[2]:
		return TierAgentic
	case score >= th[1]:
		return TierComplex
	case score >= th[0]:
		return TierStandard
	default:
		return TierSimple
	}
}

// Score is the result of a single complexity classification.
type Score struct {
	Tier  Tier    `json:"tier"`
	Score float64 `json:"score"` // 0..1 heuristic complexity
	Cause string  `json:"cause"` // which classifier produced the result
	Dims  Dims    `json:"dims"`
}

// Dims holds the per-dimension contributions (0..1 each) for
// diagnostics and the admin state endpoint.
type Dims struct {
	TokenCount float64 `json:"token_count"`
	Code       float64 `json:"code"`
	Reasoning  float64 `json:"reasoning"`
	Technical  float64 `json:"technical"`
	Simple     float64 `json:"simple"`
	MultiStep  float64 `json:"multi_step"`
	Question   float64 `json:"question"`
}

// ComplexityScorer classifies a prompt into a complexity tier.
// The heuristic scorer is the default implementation; the
// optional LLM classifier (llm.go) implements the same interface
// and falls back to the heuristic when unavailable.
type ComplexityScorer interface {
	Classify(text string) Score
}

// heuristicScorer is the zero-dependency heuristic classifier.
// It scores seven independent dimensions, combines them with
// fixed weights, and maps the result to a tier.
type heuristicScorer struct {
	thresholds Thresholds
}

// NewHeuristicScorer returns the heuristic classifier with the
// given tier thresholds.
func NewHeuristicScorer(th Thresholds) *heuristicScorer {
	return &heuristicScorer{thresholds: th}
}

// Cause values reported by the heuristic classifier.
const (
	CauseHeuristic = "heuristic"
	CauseEmpty     = "empty"
)

// Weights for the seven dimensions; they sum to 1.0 (the Simple
// penalty is subtracted separately). Token/step/question terms
// escalate long, planned, open-ended requests; code and reasoning
// terms catch hard technical content; Simple pulls short
// greetings and chit-chat back down.
const (
	wToken    = 0.30
	wCode     = 0.25
	wReason   = 0.20
	wTech     = 0.10
	wSteps    = 0.10
	wQuestion = 0.05
	wSimple   = 0.20 // subtracted
)

// Keyword/regex signals. All patterns are ASCII-only on purpose:
// Chinese and other non-ASCII text simply produces no marker hits
// and is scored by length (token count) alone, which keeps the
// classifier deterministic across languages without a dictionary.
var (
	codePattern = regexp.MustCompile(`(?i)(\x60{3}|=>|\bfunc(?:tion)?\b|\bdef\b|\bclass\b|\bstruct\b|\binterface\b|\bimport\b|\bpackage\b|\bconst\b|\bvar\b|\blet\b|\breturn\b|\bpublic\b|\bprivate\b|\bstatic\b|\btry\b|\bcatch\b|\bthrow\b|\blambda\b|\bdeclare\b)`)

	reasoningPattern = regexp.MustCompile(`(?i)(\bbecause\b|\btherefore\b|\bthus\b|\bhence\b|\bproof\b|\bprove\b|\bderive\b|\bdeduce\b|\bexplain\b|\bwhy\b|\bhow\b|\banalyze\b|\banalyse\b|\bevaluate\b|\bcompare\b|\bcontrast\b|\bjustify\b|\bimplications\b|\bscenario\b|\bassume\b|\bconsequence\b|\bdepend\b|edge case|trade-off|step by step|what if|\bsynthesi\w*|\bhypothesi\w*)`)

	technicalPattern = regexp.MustCompile(`(?i)(\bapi\b|\bendpoint\b|\bdatabase\b|\bsql\b|\bquery\b|\btransaction\b|\bschema\b|\bdeadlock\b|\bmutex\b|\bgoroutine\b|\bthread\b|\bcompiler\b|\bkernel\b|\bprotocol\b|\blatency\b|\bthroughput\b|\bcache\b|\brecursion\b|\balgorithm\b|\bcomplexity\b|o\(n\)|\bsdk\b|\bcli\b|\bhttp\b|\bjson\b|\byaml\b|\boauth\b|\bjwt\b|\btls\b|memory leak|\bregression\b|\bdeployment\b|\bcontainer\b|\bmicroservice\b|\bkubernetes\b|\bk8s\b|\bobservability\b|\btelemetry\b|\bmetric\w*|\blogging\b|graceful shutdown|\breplica\b|\bshard\b|\bindex\b|\bjoin\b|backpressure|dead letter)`)

	simplePattern = regexp.MustCompile(`(?i)(\bhello\b|\bhi\b|\bhey\b|\bthanks\b|thank you|\bok\b|\bokay\b|\bsure\b|\bbye\b|\bgreetings\b|\bgood morning\b|\bgood evening\b|\bhow are you\b)`)

	multiStepPattern = regexp.MustCompile(`(?i)(step \d|\bsteps?\b|\bfirst\b|\bsecond\b|\bthird\b|\bthen\b|\bfinally\b|\badditionally\b|\bmoreover\b|\bfurthermore\b|\boutline\b|\bplan\b|\bprocedure\b|\binstructions?\b|\bsequence\b|\benumerate\b)`)

	numberedListPattern = regexp.MustCompile(`(?m)^\s*\d+[.)]`)

	openQuestionPattern = regexp.MustCompile(`(?i)(\bwhy\b|\bhow\b|\bexplain\b|\bcompare\b|\bevaluate\b|\banalyze\b|\banalyse\b|\bdescribe\b|\bdiscuss\b|\bjustify\b|\bdesign\b|\bimplement\b|\bdebug\b|\bfix\b|\brefactor\b|\boptimize\b|\boptimise\b|\brecommend\b|best practice|trade-off|\balternative\w*|\bdifference\w*|edge case)`)

	closedQuestionPattern = regexp.MustCompile(`(?i)(is it|are there|do you|does it|can you|yes or no|true or false|is there|is this)`)
)

// tokenEstimate approximates token count: non-ASCII runes (CJK)
// count as one token each, ASCII text at ~4 chars/token. Without
// a real tokenizer this gives Chinese prompts a fair length
// signal instead of underweighting them.
func tokenEstimate(text string) int {
	var ascii, other int
	for _, r := range text {
		if r < 128 {
			ascii++
		} else {
			other++
		}
	}
	return other + (ascii+3)/4
}

// count returns the number of non-overlapping pattern matches.
func count(re *regexp.Regexp, text string) int {
	return len(re.FindAllStringIndex(text, -1))
}

// Classify scores text across seven dimensions and maps the
// weighted sum to a tier.
func (h *heuristicScorer) Classify(text string) Score {
	text = strings.TrimSpace(text)
	if text == "" {
		return Score{Tier: TierSimple, Score: 0, Cause: CauseEmpty}
	}

	code := clampRatio(count(codePattern, text), 3)
	reason := clampRatio(count(reasoningPattern, text), 3)
	tech := clampRatio(count(technicalPattern, text), 3)
	steps := clampRatio(count(multiStepPattern, text)+count(numberedListPattern, text), 3)

	dims := Dims{
		TokenCount: ramp(float64(tokenEstimate(text)), 20, 1200),
		Code:       code,
		Reasoning:  reason,
		Technical:  tech,
		Simple:     suppressSimple(simpleSignal(text, tokenEstimate(text)), code, reason, tech, steps),
		MultiStep:  steps,
		Question:   questionSignal(text),
	}

	raw := wToken*dims.TokenCount +
		wCode*dims.Code +
		wReason*dims.Reasoning +
		wTech*dims.Technical +
		wSteps*dims.MultiStep +
		wQuestion*dims.Question -
		wSimple*dims.Simple
	score := math.Max(0, math.Min(1, raw))

	return Score{
		Tier:  MapTier(score, h.thresholds),
		Score: score,
		Cause: CauseHeuristic,
		Dims:  dims,
	}
}

// suppressSimple scales the shortness penalty down once strong
// content signals fire: a short snippet with a code fence is not
// chit-chat, so it must not be pushed back into the simple tier.
func suppressSimple(simple, code, reason, tech, steps float64) float64 {
	content := math.Max(math.Max(code, reason), math.Max(tech, steps))
	switch {
	case content >= 0.6:
		return 0
	case content > 0:
		return 0.5 * simple
	default:
		return simple
	}
}

// ramp maps x to [0,1], flat below lo and saturating at hi.
func ramp(x, lo, hi float64) float64 {
	if x <= lo {
		return 0
	}
	if x >= hi {
		return 1
	}
	return (x - lo) / (hi - lo)
}

// clampRatio maps hit counts to [0,1]: hits/cap, saturated.
func clampRatio(hits, cap int) float64 {
	if hits <= 0 {
		return 0
	}
	if hits >= cap {
		return 1
	}
	return float64(hits) / float64(cap)
}

// simpleSignal is the anti-complexity dimension: short messages
// and greetings push the final score down.
func simpleSignal(text string, tokens int) float64 {
	shortness := ramp(float64(120-tokens), 0, 90) // 120 tokens -> 0, 30 -> 1
	signal := 0.7 * shortness
	if len(simplePattern.FindAllString(text, -1)) > 0 {
		signal += 0.3
	}
	return math.Min(1, signal)
}

// questionSignal scores open-ended vs closed questions. Open
// markers (why/how/explain/design/...) score up to 1; a bare "?"
// with no markers is a mild 0.2; a closed yes/no question is a
// mild 0.1.
func questionSignal(text string) float64 {
	open := len(openQuestionPattern.FindAllString(text, -1))
	if open > 0 {
		return clampRatio(open, 2)
	}
	if strings.Contains(text, "?") {
		return 0.2
	}
	if len(closedQuestionPattern.FindAllString(text, -1)) > 0 {
		return 0.1
	}
	return 0
}
