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
	ID          int64           `json:"id" gorm:"primaryKey"`
	Name        string          `json:"name" gorm:"size:128"`
	Description string          `json:"description" gorm:"size:512"`
	Type        GuardrailType   `json:"type" gorm:"size:32"`
	Hook        GuardrailHook   `json:"hook" gorm:"size:16"`
	OnFailure   GuardrailAction `json:"on_failure" gorm:"size:16"`
	Config      string          `json:"config" gorm:"serializer:json"`
	Priority    int             `json:"priority"`
	Enabled     bool            `json:"enabled"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// GuardrailEvent records a guardrail check result for auditing.
type GuardrailEvent struct {
	ID        int64     `json:"id" gorm:"primaryKey"`
	TokenID   int64     `json:"token_id" gorm:"index"`
	RuleID    int64     `json:"rule_id"`
	RuleName  string    `json:"rule_name" gorm:"size:128"`
	RuleType  string    `json:"rule_type" gorm:"size:32"`
	Hook      string    `json:"hook" gorm:"size:16"`
	Verdict   bool      `json:"verdict"` // true = passed, false = blocked
	Action    string    `json:"action" gorm:"size:16"`
	Detail    string    `json:"detail" gorm:"size:1024"`
	RequestIP string    `json:"request_ip" gorm:"size:64"`
	CreatedAt time.Time `json:"created_at"`
}
