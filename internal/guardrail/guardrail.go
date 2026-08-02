package guardrail

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/model"
)

// Result from a guardrail check.
type Result struct {
	Passed  bool
	Message string
	RuleID  int64
	Rule    string
}

// GuardrailEngine evaluates guardrail rules against requests/responses.
type GuardrailEngine struct {
	store GuardrailStore

	// Cached rules with pre-compiled regex patterns.
	rulesMu    sync.RWMutex
	rules      []cachedRule
	rulesEpoch uint64 // incremented on Reload
}

// cachedRule holds a guardrail rule with pre-parsed config.
type cachedRule struct {
	rule      model.GuardrailRule
	config    interface{}      // parsed config (type depends on rule type)
	compiled  []*regexp.Regexp // pre-compiled regex patterns
	words     []string         // blocked words as configured (case-sensitive matching)
	caseLower []string         // lowercased blocked words (case-insensitive matching)
	minChars  int
	maxChars  int
}

// GuardrailStore is the narrow interface for loading rules from the DB.
type GuardrailStore interface {
	GetEnabledGuardrailRules() ([]model.GuardrailRule, error)
	CreateGuardrailEvent(e *model.GuardrailEvent) error
}

// New creates a GuardrailEngine backed by the given store.
func New(st GuardrailStore) *GuardrailEngine {
	return &GuardrailEngine{store: st}
}

// Reload refreshes the cached rules from the store. Call after
// guardrail rules are created/updated/deleted via admin UI.
func (g *GuardrailEngine) Reload() error {
	rules, err := g.store.GetEnabledGuardrailRules()
	if err != nil {
		return err
	}
	cached := make([]cachedRule, 0, len(rules))
	for _, rule := range rules {
		cr := cachedRule{rule: rule}
		cr.parse()
		cached = append(cached, cr)
	}
	g.rulesMu.Lock()
	g.rules = cached
	g.rulesMu.Unlock()
	atomic.AddUint64(&g.rulesEpoch, 1)
	return nil
}

// ensureLoaded lazily initializes the cache on first use.
func (g *GuardrailEngine) ensureLoaded() {
	g.rulesMu.RLock()
	loaded := len(g.rules) > 0 || g.store == nil
	g.rulesMu.RUnlock()
	if !loaded {
		if err := g.Reload(); err != nil {
			// Deliberately fail open: the rule cache is empty and
			// every request would otherwise hard-fail. The DB
			// failure is loud (ERROR level) and Reload is retried
			// on the next ensureLoaded call, so a transient outage
			// heals itself while the operator sees the log.
			logging.Error("guardrail first load failed — rules disabled until reload succeeds",
				logging.F("error", err.Error()))
		}
	}
}

// CheckInput runs all input guardrails against the request messages.
// Returns the first failing result, or nil if all pass.
func (g *GuardrailEngine) CheckInput(ctx context.Context, messages []string, tokenID int64) *Result {
	if g.store == nil {
		return nil
	}
	g.ensureLoaded()
	g.rulesMu.RLock()
	rules := g.rules
	g.rulesMu.RUnlock()

	text := strings.Join(messages, "\n")
	for i := range rules {
		cr := &rules[i]
		if cr.rule.Hook != model.GuardrailHookInput && cr.rule.Hook != model.GuardrailHookBoth {
			continue
		}
		passed := evalCachedRule(cr, text)
		if !passed {
			g.recordEvent(tokenID, cr.rule, false, "input")
			if cr.rule.OnFailure == model.GuardrailActionFlag {
				logging.Warn("guardrail flagged input",
					logging.F("rule", cr.rule.Name),
					logging.F("hook", "input"),
				)
				continue
			}
			return &Result{
				Passed:  false,
				Message: fmt.Sprintf("guardrail %q blocked request", cr.rule.Name),
				RuleID:  cr.rule.ID,
				Rule:    cr.rule.Name,
			}
		}
	}
	return nil
}

// CheckOutput runs all output guardrails against the response.
func (g *GuardrailEngine) CheckOutput(ctx context.Context, response string, tokenID int64) *Result {
	if g.store == nil {
		return nil
	}
	g.ensureLoaded()
	g.rulesMu.RLock()
	rules := g.rules
	g.rulesMu.RUnlock()

	for i := range rules {
		cr := &rules[i]
		if cr.rule.Hook != model.GuardrailHookOutput && cr.rule.Hook != model.GuardrailHookBoth {
			continue
		}
		passed := evalCachedRule(cr, response)
		if !passed {
			g.recordEvent(tokenID, cr.rule, false, "output")
			if cr.rule.OnFailure == model.GuardrailActionFlag {
				logging.Warn("guardrail flagged output",
					logging.F("rule", cr.rule.Name),
					logging.F("hook", "output"),
				)
				continue
			}
			return &Result{
				Passed:  false,
				Message: fmt.Sprintf("guardrail %q blocked response", cr.rule.Name),
				RuleID:  cr.rule.ID,
				Rule:    cr.rule.Name,
			}
		}
	}
	return nil
}

func (g *GuardrailEngine) recordEvent(tokenID int64, rule model.GuardrailRule, passed bool, hook string) {
	if g.store == nil {
		return
	}
	event := &model.GuardrailEvent{
		TokenID:  tokenID,
		RuleID:   rule.ID,
		RuleName: rule.Name,
		RuleType: string(rule.Type),
		Hook:     hook,
		Verdict:  passed,
		Action:   string(rule.OnFailure),
	}
	if err := g.store.CreateGuardrailEvent(event); err != nil {
		logging.Warn("guardrail record event failed", logging.F("error", err.Error()))
	}
}

// parse pre-parses the rule config at load time.
func (cr *cachedRule) parse() {
	switch cr.rule.Type {
	case model.GuardrailRegexBlock:
		var cfg struct {
			Patterns []string `json:"patterns"`
		}
		if err := json.Unmarshal([]byte(cr.rule.Config), &cfg); err != nil {
			return
		}
		cr.compiled = make([]*regexp.Regexp, 0, len(cfg.Patterns))
		for _, p := range cfg.Patterns {
			re, err := regexp.Compile(p)
			if err != nil {
				continue
			}
			cr.compiled = append(cr.compiled, re)
		}

	case model.GuardrailBlockedWords:
		var cfg struct {
			Words         []string `json:"words"`
			CaseSensitive bool     `json:"case_sensitive"`
		}
		if err := json.Unmarshal([]byte(cr.rule.Config), &cfg); err != nil {
			return
		}
		cr.config = cfg
		cr.words = append([]string(nil), cfg.Words...)
		cr.caseLower = make([]string, 0, len(cfg.Words))
		for _, w := range cfg.Words {
			cr.caseLower = append(cr.caseLower, strings.ToLower(w))
		}

	case model.GuardrailContentLength:
		var cfg struct {
			MinChars int `json:"min_chars"`
			MaxChars int `json:"max_chars"`
		}
		if err := json.Unmarshal([]byte(cr.rule.Config), &cfg); err != nil {
			return
		}
		cr.minChars = cfg.MinChars
		cr.maxChars = cfg.MaxChars
	}
}

// evalCachedRule evaluates a pre-parsed rule against text.
func evalCachedRule(cr *cachedRule, text string) bool {
	switch cr.rule.Type {
	case model.GuardrailRegexBlock:
		for _, re := range cr.compiled {
			if re.MatchString(text) {
				return false
			}
		}
		return true

	case model.GuardrailBlockedWords:
		cfg, ok := cr.config.(struct {
			Words         []string `json:"words"`
			CaseSensitive bool     `json:"case_sensitive"`
		})
		if !ok {
			return true
		}
		compare := text
		words := cr.caseLower
		if !cfg.CaseSensitive {
			compare = strings.ToLower(text)
		} else {
			// Case-sensitive: match the original words against the
			// original text. (Previously the lowercased word list
			// was used here too, so words containing uppercase
			// letters could never match.)
			words = cr.words
		}
		for _, word := range words {
			if strings.Contains(compare, word) {
				return false
			}
		}
		return true

	case model.GuardrailContentLength:
		length := len(text)
		if cr.minChars > 0 && length < cr.minChars {
			return false
		}
		if cr.maxChars > 0 && length > cr.maxChars {
			return false
		}
		return true

	default:
		return true
	}
}

// evaluateRule dispatches to the appropriate check function (legacy path).
func evaluateRule(rule model.GuardrailRule, text string) bool {
	switch rule.Type {
	case model.GuardrailRegexBlock:
		return checkRegexBlock(rule.Config, text)
	case model.GuardrailBlockedWords:
		return checkBlockedWords(rule.Config, text)
	case model.GuardrailContentLength:
		return checkContentLength(rule.Config, text)
	default:
		return true
	}
}

// checkRegexBlock blocks text matching any of the configured patterns.
func checkRegexBlock(configJSON, text string) bool {
	var cfg struct {
		Patterns []string `json:"patterns"`
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return true
	}
	for _, p := range cfg.Patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		if re.MatchString(text) {
			return false
		}
	}
	return true
}

// checkBlockedWords blocks text containing any of the configured words.
func checkBlockedWords(configJSON, text string) bool {
	var cfg struct {
		Words         []string `json:"words"`
		CaseSensitive bool     `json:"case_sensitive"`
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return true
	}
	compare := text
	if !cfg.CaseSensitive {
		compare = strings.ToLower(text)
	}
	for _, w := range cfg.Words {
		if !cfg.CaseSensitive {
			w = strings.ToLower(w)
		}
		if strings.Contains(compare, w) {
			return false
		}
	}
	return true
}

// checkContentLength blocks text outside the configured min/max bounds.
func checkContentLength(configJSON, text string) bool {
	var cfg struct {
		MinChars int `json:"min_chars"`
		MaxChars int `json:"max_chars"`
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return true
	}
	length := len(text)
	if cfg.MinChars > 0 && length < cfg.MinChars {
		return false
	}
	if cfg.MaxChars > 0 && length > cfg.MaxChars {
		return false
	}
	return true
}
