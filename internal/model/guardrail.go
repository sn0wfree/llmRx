package model

import "time"

// GuardrailType defines the types of guardrail checks available.
type GuardrailType string

const (
	GuardrailRegexBlock    GuardrailType = "regex_block"
	GuardrailBlockedWords  GuardrailType = "blocked_words"
	GuardrailContentLength GuardrailType = "content_length"
)

// GuardrailHook defines when a guardrail check runs.
type GuardrailHook string

const (
	GuardrailHookInput  GuardrailHook = "input"
	GuardrailHookOutput GuardrailHook = "output"
	GuardrailHookBoth   GuardrailHook = "both"
)

// GuardrailAction defines what happens when a guardrail fails.
type GuardrailAction string

const (
	GuardrailActionDeny GuardrailAction = "deny"
	GuardrailActionFlag GuardrailAction = "flag"
)

// GuardrailRule represents a single guardrail check. Rules are global
// and reusable; tokens and plans reference them by ID.
type GuardrailRule struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Type        GuardrailType   `json:"type"`
	Hook        GuardrailHook   `json:"hook"`
	OnFailure   GuardrailAction `json:"on_failure"`
	Config      string          `json:"config"`
	Priority    int             `json:"priority"`
	Enabled     bool            `json:"enabled"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// GuardrailEvent records a guardrail check result for auditing.
type GuardrailEvent struct {
	ID        int64     `json:"id"`
	TokenID   int64     `json:"token_id"`
	RuleID    int64     `json:"rule_id"`
	RuleName  string    `json:"rule_name"`
	RuleType  string    `json:"rule_type"`
	Hook      string    `json:"hook"`
	Verdict   bool      `json:"verdict"` // true = passed, false = blocked
	Action    string    `json:"action"`
	Detail    string    `json:"detail"`
	RequestIP string    `json:"request_ip"`
	CreatedAt time.Time `json:"created_at"`
}
