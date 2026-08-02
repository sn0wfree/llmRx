// Package auto implements the auto-routing tier: model
// auto-selection (complexity classification + arm sampling) and
// channel auto-scheduling for mode:auto combos.
package auto

import (
	"math"
	"strings"

	"github.com/sn0wfree/llmRx/internal/model"
)

// Tier is a complexity tier that maps a request to a candidate
// model table. Higher tiers allow more capable (and costlier)
// models. The canonical name set lives in model.AutoTiers so the
// store can validate combo configs without importing this package.
type Tier string

// The four complexity tiers, ordered from cheapest to most
// capable.
const (
	TierSimple   Tier = "simple"
	TierStandard Tier = "standard"
	TierComplex  Tier = "complex"
	TierAgentic  Tier = "agentic"
)

// AllTiers lists the tiers in ascending cost order. Kept in sync
// with model.AutoTiers (the source of truth for validation).
var AllTiers = func() []Tier {
	out := make([]Tier, 0, len(model.AutoTiers))
	for _, t := range model.AutoTiers {
		out = append(out, Tier(t))
	}
	return out
}()

// ValidTier reports whether t is one of the known tiers.
func ValidTier(t string) bool { return model.ValidAutoTier(t) }

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
//
// Matching is deliberately regexp-free: \b-heavy alternations on
// RE2 blow up to milliseconds on multi-KB prompts (DFA state
// explosion), while lowercasing + byte tokenization + map lookups
// stay in the tens of microseconds.
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

// Keyword signals. All sets are lowercase ASCII words: the input
// is lowercased once, then split into [a-z0-9]+ tokens, so CJK
// and other non-ASCII text produces no marker hits and is scored
// by length (token count) alone — deterministic across languages
// without a dictionary.
var (
	codeWords = wordSet(
		"func", "function", "def", "class", "struct", "interface", "import",
		"package", "const", "var", "let", "return", "public", "private",
		"static", "try", "catch", "throw", "lambda", "declare",
	)

	reasoningWords = wordSet(
		"because", "therefore", "thus", "hence", "proof", "prove", "derive",
		"deduce", "explain", "why", "how", "analyze", "analyse", "evaluate",
		"compare", "contrast", "justify", "implications", "scenario", "assume",
		"consequence", "depend",
	)

	technicalWords = wordSet(
		"api", "endpoint", "database", "sql", "query", "transaction", "schema",
		"deadlock", "mutex", "goroutine", "thread", "compiler", "kernel",
		"protocol", "latency", "throughput", "cache", "recursion", "algorithm",
		"complexity", "sdk", "cli", "http", "json", "yaml", "oauth", "jwt",
		"tls", "regression", "deployment", "container", "microservice",
		"kubernetes", "k8s", "observability", "telemetry", "logging", "replica",
		"shard", "index", "join",
	)

	simpleWords = wordSet(
		"hello", "hi", "hey", "thanks", "ok", "okay", "sure", "bye",
		"greetings",
	)

	multiStepWords = wordSet(
		"step", "steps", "first", "second", "third", "then", "finally",
		"additionally", "moreover", "furthermore", "outline", "plan",
		"procedure", "instruction", "instructions", "sequence", "enumerate",
	)

	openQuestionWords = wordSet(
		"why", "how", "explain", "compare", "evaluate", "analyze", "analyse",
		"describe", "discuss", "justify", "design", "implement", "debug",
		"fix", "refactor", "optimize", "optimise", "recommend",
	)

	// wordPrefixes match trailing-wildcard signals such as
	// \bsynthesi\w*: any token starting with one of these counts.
	wordPrefixes = []string{"synthesi", "hypothesi", "metric", "alternative", "difference"}

	// codePhrases / technicalPhrases / etc. hold the signals that
	// were multi-word or symbolic substrings in the original
	// regexes; matched with plain substring search on the
	// lowercased text.
	codePhrases      = []string{"```", "=>"}
	reasoningPhrases = []string{"edge case", "trade-off", "step by step", "what if"}
	technicalPhrases = []string{"o(n)", "memory leak", "graceful shutdown", "backpressure", "dead letter"}
	simplePhrases    = []string{"thank you", "how are you", "good morning", "good evening"}

	openQuestionPhrases   = []string{"best practice", "trade-off", "edge case"}
	closedQuestionPhrases = []string{
		"is it", "are there", "do you", "does it", "can you",
		"yes or no", "true or false", "is there", "is this",
	}
)

func wordSet(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// TokenEstimate approximates token count: non-ASCII runes (CJK)
// count as one token each, ASCII text at ~4 chars/token. Without
// a real tokenizer this gives Chinese prompts a fair length
// signal instead of underweighting them. Shared with the context
// budget filter (pool.go).
func TokenEstimate(text string) int {
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

// lowerTokens lowercases text once and splits it into [a-z0-9]+
// tokens (byte scan: every non-ASCII byte is a separator, so CJK
// text simply yields no tokens). The returned tokens share the
// lowercased string's backing memory.
func lowerTokens(text string) (low string, toks []string) {
	low = strings.ToLower(text)
	toks = make([]string, 0, 32)
	start := -1
	for i := 0; i < len(low); i++ {
		c := low[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			toks = append(toks, low[start:i])
			start = -1
		}
	}
	if start >= 0 {
		toks = append(toks, low[start:])
	}
	return low, toks
}

// countWords counts tokens in set, early-exiting at cap (the
// dimension scores saturate at 3 hits, so counting further is
// wasted work).
func countWords(toks []string, set map[string]bool, cap int) int {
	hits := 0
	for _, t := range toks {
		if set[t] {
			hits++
			if hits >= cap {
				return hits
			}
		}
	}
	return hits
}

// countPrefix counts tokens starting with any prefix, capped.
func countPrefix(toks []string, prefixes []string, cap int) int {
	hits := 0
	for _, t := range toks {
		for _, p := range prefixes {
			if strings.HasPrefix(t, p) {
				hits++
				if hits >= cap {
					return hits
				}
				break
			}
		}
	}
	return hits
}

// countPhrases counts distinct substring hits for phrases, capped
// at cap. Each phrase contributes at most one hit (it cannot be
// matched twice at the same location by a substring search).
func countPhrases(low string, phrases []string, cap int) int {
	hits := 0
	for _, p := range phrases {
		if strings.Contains(low, p) {
			hits++
			if hits >= cap {
				return hits
			}
		}
	}
	return hits
}

// countStepN counts "step <digits>" markers: a "step" token
// directly followed by an all-digit token.
func countStepN(toks []string) int {
	hits := 0
	for i := 0; i+1 < len(toks); i++ {
		if toks[i] == "step" && isAllDigits(toks[i+1]) {
			hits++
		}
	}
	if hits > 3 {
		return 3
	}
	return hits
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// countNumberedLines counts list items ("1." / "1)") at the start
// of lines, capped at 3 — the hand-rolled twin of the old
// (?m)^\s*\d+[.)] regex (measured ~17x faster).
func countNumberedLines(low string) int {
	hits := 0
	for len(low) > 0 {
		nl := strings.IndexByte(low, '\n')
		var line string
		if nl < 0 {
			line, low = low, ""
		} else {
			line, low = low[:nl], low[nl+1:]
		}
		j := 0
		for j < len(line) && (line[j] == ' ' || line[j] == '\t') {
			j++
		}
		k := j
		for k < len(line) && line[k] >= '0' && line[k] <= '9' {
			k++
		}
		if k > j && k < len(line) && (line[k] == '.' || line[k] == ')') {
			hits++
			if hits >= 3 {
				return hits
			}
		}
	}
	return hits
}

// Classify scores text across seven dimensions and maps the
// weighted sum to a tier.
func (h *heuristicScorer) Classify(text string) Score {
	text = strings.TrimSpace(text)
	if text == "" {
		return Score{Tier: TierSimple, Score: 0, Cause: CauseEmpty}
	}

	low, toks := lowerTokens(text)
	est := TokenEstimate(text)

	code := clampRatio(countWords(toks, codeWords, 3)+countPhrases(low, codePhrases, 3), 3)
	reason := clampRatio(countWords(toks, reasoningWords, 3)+countPhrases(low, reasoningPhrases, 3), 3)
	tech := clampRatio(countWords(toks, technicalWords, 3)+countPrefix(toks, wordPrefixes, 3)+countPhrases(low, technicalPhrases, 3), 3)
	steps := clampRatio(countWords(toks, multiStepWords, 3)+countStepN(toks)+countNumberedLines(low), 3)

	dims := Dims{
		TokenCount: ramp(float64(est), 20, 1200),
		Code:       code,
		Reasoning:  reason,
		Technical:  tech,
		Simple:     suppressSimple(simpleSignal(low, toks, est), code, reason, tech, steps),
		MultiStep:  steps,
		Question:   questionSignal(low, toks),
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
func simpleSignal(low string, toks []string, tokens int) float64 {
	shortness := ramp(float64(120-tokens), 0, 90) // 120 tokens -> 0, 30 -> 1
	signal := 0.7 * shortness
	if countWords(toks, simpleWords, 1) > 0 || countPhrases(low, simplePhrases, 1) > 0 {
		signal += 0.3
	}
	return math.Min(1, signal)
}

// questionSignal scores open-ended vs closed questions. Open
// markers (why/how/explain/design/...) score up to 1; a bare "?"
// with no markers is a mild 0.2; a closed yes/no question is a
// mild 0.1.
func questionSignal(low string, toks []string) float64 {
	open := countWords(toks, openQuestionWords, 2) + countPhrases(low, openQuestionPhrases, 2)
	if open > 0 {
		return clampRatio(open, 2)
	}
	if strings.Contains(low, "?") {
		return 0.2
	}
	if countPhrases(low, closedQuestionPhrases, 1) > 0 {
		return 0.1
	}
	return 0
}
