package guardrail

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

// mockStore implements GuardrailStore for testing.
type mockStore struct {
	rules  []model.GuardrailRule
	events []*model.GuardrailEvent
	err    error // inject error on GetEnabledGuardrailRules
}

func (m *mockStore) GetEnabledGuardrailRules() ([]model.GuardrailRule, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rules, nil
}

func (m *mockStore) CreateGuardrailEvent(e *model.GuardrailEvent) error {
	m.events = append(m.events, e)
	return nil
}

func configJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// --- evaluateRule ---

func TestEvaluateRule_UnknownType(t *testing.T) {
	rule := model.GuardrailRule{Type: "unknown_type"}
	if !evaluateRule(rule, "anything") {
		t.Fatal("unknown rule type should pass")
	}
}

// --- checkRegexBlock ---

func TestRegexBlock_Match(t *testing.T) {
	cfg := configJSON(map[string]interface{}{"patterns": []string{`\b\d{3}-\d{2}-\d{4}\b`}})
	if checkRegexBlock(cfg, "my ssn is 123-45-6789") {
		t.Fatal("should block SSN pattern")
	}
}

func TestRegexBlock_NoMatch(t *testing.T) {
	cfg := configJSON(map[string]interface{}{"patterns": []string{`\b\d{3}-\d{2}-\d{4}\b`}})
	if !checkRegexBlock(cfg, "hello world") {
		t.Fatal("should pass when no pattern matches")
	}
}

func TestRegexBlock_MultiplePatterns(t *testing.T) {
	cfg := configJSON(map[string]interface{}{"patterns": []string{`secret`, `password`}})
	if checkRegexBlock(cfg, "my secret code") {
		t.Fatal("should block first matching pattern")
	}
	if checkRegexBlock(cfg, "my password is 123") {
		t.Fatal("should block second matching pattern")
	}
	if !checkRegexBlock(cfg, "hello world") {
		t.Fatal("should pass when no pattern matches")
	}
}

func TestRegexBlock_BadJSON(t *testing.T) {
	if !checkRegexBlock("not json!!!", "text") {
		t.Fatal("bad config JSON should pass")
	}
}

func TestRegexBlock_InvalidPattern(t *testing.T) {
	cfg := configJSON(map[string]interface{}{"patterns": []string{`[invalid`}})
	if !checkRegexBlock(cfg, "text") {
		t.Fatal("invalid regex pattern should be skipped (pass)")
	}
}

func TestRegexBlock_EmptyPatterns(t *testing.T) {
	cfg := configJSON(map[string]interface{}{"patterns": []string{}})
	if !checkRegexBlock(cfg, "text") {
		t.Fatal("empty patterns should pass")
	}
}

// --- checkBlockedWords ---

func TestBlockedWords_CaseInsensitive(t *testing.T) {
	cfg := configJSON(map[string]interface{}{"words": []string{"badword"}, "case_sensitive": false})
	if checkBlockedWords(cfg, "This has BADWORD in it") {
		t.Fatal("should block case-insensitive match")
	}
	if !checkBlockedWords(cfg, "hello world") {
		t.Fatal("should pass when no word matches")
	}
}

func TestBlockedWords_CaseSensitive(t *testing.T) {
	cfg := configJSON(map[string]interface{}{"words": []string{"BadWord"}, "case_sensitive": true})
	if !checkBlockedWords(cfg, "This has BADWORD in it") {
		t.Fatal("should not match case-sensitive different")
	}
	if checkBlockedWords(cfg, "This has BadWord in it") {
		t.Fatal("should block exact case match")
	}
}

func TestBlockedWords_MultipleWords(t *testing.T) {
	cfg := configJSON(map[string]interface{}{"words": []string{"alpha", "beta"}})
	if checkBlockedWords(cfg, "alpha is here") {
		t.Fatal("should block first word")
	}
	if checkBlockedWords(cfg, "beta is here") {
		t.Fatal("should block second word")
	}
	if !checkBlockedWords(cfg, "gamma is here") {
		t.Fatal("should pass when no word matches")
	}
}

func TestBlockedWords_BadJSON(t *testing.T) {
	if !checkBlockedWords("not json!!!", "text") {
		t.Fatal("bad config JSON should pass")
	}
}

func TestBlockedWords_EmptyWords(t *testing.T) {
	cfg := configJSON(map[string]interface{}{"words": []string{}})
	if !checkBlockedWords(cfg, "text") {
		t.Fatal("empty words should pass")
	}
}

// --- checkContentLength ---

func TestContentLength_WithinBounds(t *testing.T) {
	cfg := configJSON(map[string]interface{}{"min_chars": 5, "max_chars": 20})
	if !checkContentLength(cfg, "hello") {
		t.Fatal("5 chars should pass min_chars=5")
	}
	if !checkContentLength(cfg, "12345678901234567890") {
		t.Fatal("20 chars should pass max_chars=20")
	}
}

func TestContentLength_TooShort(t *testing.T) {
	cfg := configJSON(map[string]interface{}{"min_chars": 10})
	if checkContentLength(cfg, "short") {
		t.Fatal("5 chars should fail min_chars=10")
	}
}

func TestContentLength_TooLong(t *testing.T) {
	cfg := configJSON(map[string]interface{}{"max_chars": 5})
	if checkContentLength(cfg, "toolongtext") {
		t.Fatal("11 chars should fail max_chars=5")
	}
}

func TestContentLength_OnlyMin(t *testing.T) {
	cfg := configJSON(map[string]interface{}{"min_chars": 5})
	if checkContentLength(cfg, "hi") {
		t.Fatal("2 chars should fail min_chars=5")
	}
	if !checkContentLength(cfg, "very long text") {
		t.Fatal("should pass with no max limit")
	}
}

func TestContentLength_OnlyMax(t *testing.T) {
	cfg := configJSON(map[string]interface{}{"max_chars": 5})
	if !checkContentLength(cfg, "hi") {
		t.Fatal("2 chars should pass max_chars=5")
	}
	if checkContentLength(cfg, "toolong") {
		t.Fatal("7 chars should fail max_chars=5")
	}
}

func TestContentLength_BadJSON(t *testing.T) {
	if !checkContentLength("not json!!!", "text") {
		t.Fatal("bad config JSON should pass")
	}
}

func TestContentLength_ZeroBounds(t *testing.T) {
	cfg := configJSON(map[string]interface{}{"min_chars": 0, "max_chars": 0})
	if !checkContentLength(cfg, "anything") {
		t.Fatal("zero bounds should pass everything")
	}
}

// --- CheckInput ---

func TestCheckInput_NilStore(t *testing.T) {
	e := New(nil)
	if r := e.CheckInput(context.Background(), []string{"hello"}, 1); r != nil {
		t.Fatal("nil store should return nil")
	}
}

func TestCheckInput_StoreError(t *testing.T) {
	m := &mockStore{err: errors.New("db down")}
	e := New(m)
	if r := e.CheckInput(context.Background(), []string{"hello"}, 1); r != nil {
		t.Fatal("store error should return nil (non-blocking)")
	}
}

func TestCheckInput_AllPass(t *testing.T) {
	m := &mockStore{rules: []model.GuardrailRule{
		{ID: 1, Name: "r1", Type: model.GuardrailRegexBlock, Hook: model.GuardrailHookInput, Config: configJSON(map[string]interface{}{"patterns": []string{`secret`}})},
	}}
	e := New(m)
	if r := e.CheckInput(context.Background(), []string{"hello world"}, 1); r != nil {
		t.Fatalf("should pass: %v", r)
	}
}

func TestCheckInput_Blocked(t *testing.T) {
	m := &mockStore{rules: []model.GuardrailRule{
		{ID: 1, Name: "block_secret", Type: model.GuardrailRegexBlock, Hook: model.GuardrailHookInput, Config: configJSON(map[string]interface{}{"patterns": []string{`secret`}})},
	}}
	e := New(m)
	r := e.CheckInput(context.Background(), []string{"my secret data"}, 1)
	if r == nil {
		t.Fatal("should block")
	}
	if r.Passed {
		t.Fatal("Passed should be false")
	}
	if r.Rule != "block_secret" {
		t.Fatalf("rule name: got %q", r.Rule)
	}
	if r.RuleID != 1 {
		t.Fatalf("rule id: got %d", r.RuleID)
	}
	if len(m.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(m.events))
	}
	if m.events[0].Verdict {
		t.Fatal("event verdict should be false (blocked)")
	}
}

func TestCheckInput_OutputHookSkipped(t *testing.T) {
	m := &mockStore{rules: []model.GuardrailRule{
		{ID: 1, Name: "output_only", Type: model.GuardrailRegexBlock, Hook: model.GuardrailHookOutput, Config: configJSON(map[string]interface{}{"patterns": []string{`secret`}})},
	}}
	e := New(m)
	if r := e.CheckInput(context.Background(), []string{"my secret data"}, 1); r != nil {
		t.Fatal("output hook should be skipped on input check")
	}
}

func TestCheckInput_BothHook(t *testing.T) {
	m := &mockStore{rules: []model.GuardrailRule{
		{ID: 1, Name: "both", Type: model.GuardrailBlockedWords, Hook: model.GuardrailHookBoth, Config: configJSON(map[string]interface{}{"words": []string{"bad"}})},
	}}
	e := New(m)
	r := e.CheckInput(context.Background(), []string{"this is bad"}, 1)
	if r == nil {
		t.Fatal("both hook should run on input")
	}
}

func TestCheckInput_MultipleMessagesJoined(t *testing.T) {
	m := &mockStore{rules: []model.GuardrailRule{
		{ID: 1, Name: "r", Type: model.GuardrailRegexBlock, Hook: model.GuardrailHookInput, Config: configJSON(map[string]interface{}{"patterns": []string{`cross_msg`}})},
	}}
	e := New(m)
	if r := e.CheckInput(context.Background(), []string{"msg1", "cross_msg here"}, 1); r == nil {
		t.Fatal("should match across joined messages")
	}
}

func TestCheckInput_MultipleRules_FirstFailsWins(t *testing.T) {
	m := &mockStore{rules: []model.GuardrailRule{
		{ID: 1, Name: "block_a", Type: model.GuardrailBlockedWords, Hook: model.GuardrailHookInput, Config: configJSON(map[string]interface{}{"words": []string{"aaa"}})},
		{ID: 2, Name: "block_b", Type: model.GuardrailBlockedWords, Hook: model.GuardrailHookInput, Config: configJSON(map[string]interface{}{"words": []string{"bbb"}})},
	}}
	e := New(m)
	r := e.CheckInput(context.Background(), []string{"aaa bbb"}, 1)
	if r == nil {
		t.Fatal("should block")
	}
	if r.Rule != "block_a" {
		t.Fatalf("first failing rule should win: got %q", r.Rule)
	}
}

func TestCheckInput_PriorityOrder(t *testing.T) {
	m := &mockStore{rules: []model.GuardrailRule{
		{ID: 2, Name: "low_priority", Type: model.GuardrailBlockedWords, Hook: model.GuardrailHookInput, Config: configJSON(map[string]interface{}{"words": []string{"low"}}), Priority: 100},
		{ID: 1, Name: "high_priority", Type: model.GuardrailBlockedWords, Hook: model.GuardrailHookInput, Config: configJSON(map[string]interface{}{"words": []string{"high"}}), Priority: 1},
	}}
	e := New(m)
	r := e.CheckInput(context.Background(), []string{"high"}, 1)
	if r == nil {
		t.Fatal("should block")
	}
	if r.Rule != "high_priority" {
		t.Fatalf("higher priority (lower number) should run first: got %q", r.Rule)
	}
}

// --- CheckOutput ---

func TestCheckOutput_NilStore(t *testing.T) {
	e := New(nil)
	if r := e.CheckOutput(context.Background(), "hello", 1); r != nil {
		t.Fatal("nil store should return nil")
	}
}

func TestCheckOutput_AllPass(t *testing.T) {
	m := &mockStore{rules: []model.GuardrailRule{
		{ID: 1, Name: "r1", Type: model.GuardrailRegexBlock, Hook: model.GuardrailHookOutput, Config: configJSON(map[string]interface{}{"patterns": []string{`secret`}})},
	}}
	e := New(m)
	if r := e.CheckOutput(context.Background(), "safe response", 1); r != nil {
		t.Fatalf("should pass: %v", r)
	}
}

func TestCheckOutput_Blocked(t *testing.T) {
	m := &mockStore{rules: []model.GuardrailRule{
		{ID: 1, Name: "block_leak", Type: model.GuardrailBlockedWords, Hook: model.GuardrailHookOutput, Config: configJSON(map[string]interface{}{"words": []string{"api_key"}})},
	}}
	e := New(m)
	r := e.CheckOutput(context.Background(), "here is your api_key: sk-xxx", 42)
	if r == nil {
		t.Fatal("should block")
	}
	if r.Rule != "block_leak" {
		t.Fatalf("rule: got %q", r.Rule)
	}
	if len(m.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(m.events))
	}
	if m.events[0].TokenID != 42 {
		t.Fatalf("event token_id: got %d", m.events[0].TokenID)
	}
}

func TestCheckOutput_InputHookSkipped(t *testing.T) {
	m := &mockStore{rules: []model.GuardrailRule{
		{ID: 1, Name: "input_only", Type: model.GuardrailRegexBlock, Hook: model.GuardrailHookInput, Config: configJSON(map[string]interface{}{"patterns": []string{`secret`}})},
	}}
	e := New(m)
	if r := e.CheckOutput(context.Background(), "secret data", 1); r != nil {
		t.Fatal("input hook should be skipped on output check")
	}
}

func TestCheckOutput_BothHook(t *testing.T) {
	m := &mockStore{rules: []model.GuardrailRule{
		{ID: 1, Name: "both", Type: model.GuardrailBlockedWords, Hook: model.GuardrailHookBoth, Config: configJSON(map[string]interface{}{"words": []string{"blocked"}})},
	}}
	e := New(m)
	r := e.CheckOutput(context.Background(), "this is blocked", 1)
	if r == nil {
		t.Fatal("both hook should run on output")
	}
}

func TestCheckOutput_StoreError(t *testing.T) {
	m := &mockStore{err: errors.New("db down")}
	e := New(m)
	if r := e.CheckOutput(context.Background(), "anything", 1); r != nil {
		t.Fatal("store error should return nil")
	}
}

func TestCheckOutput_ContentLength(t *testing.T) {
	m := &mockStore{rules: []model.GuardrailRule{
		{ID: 1, Name: "max_len", Type: model.GuardrailContentLength, Hook: model.GuardrailHookOutput, Config: configJSON(map[string]interface{}{"max_chars": 10})},
	}}
	e := New(m)
	if r := e.CheckOutput(context.Background(), "short", 1); r != nil {
		t.Fatal("short should pass")
	}
	if r := e.CheckOutput(context.Background(), "this is way too long", 1); r == nil {
		t.Fatal("long text should be blocked")
	}
}

// --- recordEvent ---

func TestRecordEvent_NilStore(t *testing.T) {
	e := New(nil)
	e.recordEvent(1, model.GuardrailRule{}, false, "input") // should not panic
}

// --- Benchmark ---

func BenchmarkEvaluateRule(b *testing.B) {
	rule := model.GuardrailRule{
		Type:   model.GuardrailRegexBlock,
		Config: configJSON(map[string]interface{}{"patterns": []string{`\b\d{3}-\d{2}-\d{4}\b`}}),
	}
	text := "my ssn is 123-45-6789 and my name is John"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		evaluateRule(rule, text)
	}
}

func BenchmarkCheckBlockedWords(b *testing.B) {
	cfg := configJSON(map[string]interface{}{"words": []string{"alpha", "beta", "gamma", "delta"}})
	text := "the quick brown fox jumps over the lazy dog"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		checkBlockedWords(cfg, text)
	}
}

func BenchmarkCheckInput(b *testing.B) {
	m := &mockStore{rules: []model.GuardrailRule{
		{ID: 1, Name: "r1", Type: model.GuardrailRegexBlock, Hook: model.GuardrailHookInput, Config: configJSON(map[string]interface{}{"patterns": []string{`secret`}})},
		{ID: 2, Name: "r2", Type: model.GuardrailBlockedWords, Hook: model.GuardrailHookInput, Config: configJSON(map[string]interface{}{"words": []string{"badword"}})},
		{ID: 3, Name: "r3", Type: model.GuardrailContentLength, Hook: model.GuardrailHookInput, Config: configJSON(map[string]interface{}{"max_chars": 10000})},
	}}
	e := New(m)
	ctx := context.Background()
	msgs := []string{"hello world, this is a safe message"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.CheckInput(ctx, msgs, 1)
	}
}

// TestEvalCachedRule_BlockedWords_CaseSensitive: a word containing
// uppercase letters must actually match when case_sensitive is true
// (regression: the lowercased word list was matched against the
// original text, so uppercase words could never hit).
func TestEvalCachedRule_BlockedWords_CaseSensitive(t *testing.T) {
	cr := &cachedRule{rule: model.GuardrailRule{
		Type:   model.GuardrailBlockedWords,
		Config: `{"words":["BadWord"],"case_sensitive":true}`,
	}}
	cr.parse()
	if evalCachedRule(cr, "this has BadWord in it") {
		t.Fatal("exact-case word must block")
	}
	if !evalCachedRule(cr, "this has BADWORD in it") {
		t.Fatal("different case must pass when case_sensitive")
	}
}

// TestEvalCachedRule_BlockedWords_CaseInsensitive: lowercasing both
// sides still matches mixed-case text.
func TestEvalCachedRule_BlockedWords_CaseInsensitive(t *testing.T) {
	cr := &cachedRule{rule: model.GuardrailRule{
		Type:   model.GuardrailBlockedWords,
		Config: `{"words":["badword"],"case_sensitive":false}`,
	}}
	cr.parse()
	if evalCachedRule(cr, "this has BaDwOrD in it") {
		t.Fatal("mixed-case text must block when insensitive")
	}
	if !evalCachedRule(cr, "clean text") {
		t.Fatal("clean text must pass")
	}
}

// TestEvalCachedRule_BlockedWords_BadConfig: unparseable config
// fails open (matches evalCachedRule's existing behavior).
func TestEvalCachedRule_BlockedWords_BadConfig(t *testing.T) {
	cr := &cachedRule{rule: model.GuardrailRule{
		Type:   model.GuardrailBlockedWords,
		Config: `not json`,
	}}
	cr.parse()
	if !evalCachedRule(cr, "anything") {
		t.Fatal("bad config should fail open")
	}
}

// TestEnsureLoaded_EmptyRulesLoadsOnce: with zero rules the store
// must be queried exactly once — the per-request re-query bug
// (len(rules)==0 treated as "not loaded") regresses this test.
func TestEnsureLoaded_EmptyRulesLoadsOnce(t *testing.T) {
	counting := &countingStore{inner: &mockStore{rules: nil}}
	g2 := New(counting)
	for i := 0; i < 100; i++ {
		g2.CheckInput(context.Background(), []string{"hi"}, 1)
	}
	if calls := atomic.LoadInt32(&counting.calls); calls != 1 {
		t.Fatalf("store queried %d times, want 1", calls)
	}
}

type countingStore struct {
	inner GuardrailStore
	calls int32
}

func (c *countingStore) GetEnabledGuardrailRules() ([]model.GuardrailRule, error) {
	atomic.AddInt32(&c.calls, 1)
	return c.inner.GetEnabledGuardrailRules()
}

func (c *countingStore) CreateGuardrailEvent(e *model.GuardrailEvent) error {
	return c.inner.CreateGuardrailEvent(e)
}
