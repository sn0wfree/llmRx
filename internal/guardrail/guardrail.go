package guardrail

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

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

// CheckInput runs all input guardrails against the request messages.
// Returns the first failing result, or nil if all pass.
func (g *GuardrailEngine) CheckInput(ctx context.Context, messages []string, tokenID int64) *Result {
	if g.store == nil {
		return nil
	}
	rules, err := g.store.GetEnabledGuardrailRules()
	if err != nil {
		logging.Warn("guardrail load rules failed", logging.F("error", err.Error()))
		return nil
	}
	text := strings.Join(messages, "\n")
	for _, rule := range rules {
		if rule.Hook != model.GuardrailHookInput && rule.Hook != model.GuardrailHookBoth {
			continue
		}
		passed := evaluateRule(rule, text)
		if !passed {
			g.recordEvent(tokenID, rule, false, "input")
			if rule.OnFailure == model.GuardrailActionFlag {
				logging.Warn("guardrail flagged input",
					logging.F("rule", rule.Name),
					logging.F("hook", "input"),
				)
				continue
			}
			return &Result{
				Passed:  false,
				Message: fmt.Sprintf("guardrail %q blocked request", rule.Name),
				RuleID:  rule.ID,
				Rule:    rule.Name,
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
	rules, err := g.store.GetEnabledGuardrailRules()
	if err != nil {
		logging.Warn("guardrail load rules failed", logging.F("error", err.Error()))
		return nil
	}
	for _, rule := range rules {
		if rule.Hook != model.GuardrailHookOutput && rule.Hook != model.GuardrailHookBoth {
			continue
		}
		passed := evaluateRule(rule, response)
		if !passed {
			g.recordEvent(tokenID, rule, false, "output")
			if rule.OnFailure == model.GuardrailActionFlag {
				logging.Warn("guardrail flagged output",
					logging.F("rule", rule.Name),
					logging.F("hook", "output"),
				)
				continue
			}
			return &Result{
				Passed:  false,
				Message: fmt.Sprintf("guardrail %q blocked response", rule.Name),
				RuleID:  rule.ID,
				Rule:    rule.Name,
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

// evaluateRule dispatches to the appropriate check function.
func evaluateRule(rule model.GuardrailRule, text string) bool {
	switch rule.Type {
	case model.GuardrailRegexBlock:
		return checkRegexBlock(rule.Config, text)
	case model.GuardrailBlockedWords:
		return checkBlockedWords(rule.Config, text)
	case model.GuardrailContentLength:
		return checkContentLength(rule.Config, text)
	default:
		return true // unknown rule type = pass
	}
}

// checkRegexBlock blocks text matching any of the configured patterns.
func checkRegexBlock(configJSON, text string) bool {
	var cfg struct {
		Patterns []string `json:"patterns"`
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return true // bad config = pass
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
