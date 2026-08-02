package store

import (
	"errors"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

// ──────────────────────────────────────────────────────────
// Guardrails CRUD
// ──────────────────────────────────────────────────────────

func TestGuardrails_CRUD(t *testing.T) {
	s := openTemp(t)
	r := &model.GuardrailRule{
		Name:        "block-injection",
		Description: "blocks prompt injection patterns",
		Type:        model.GuardrailRegexBlock,
		Hook:        model.GuardrailHookInput,
		OnFailure:   model.GuardrailActionDeny,
		Config:      `{"patterns":["ignore previous"]}`,
		Priority:    1,
		Enabled:     true,
	}
	if err := s.CreateGuardrailRule(r); err != nil {
		t.Fatalf("CreateGuardrailRule: %v", err)
	}
	if r.ID == 0 {
		t.Fatal("expected non-zero ID")
	}
	if r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		t.Fatal("timestamps should be set")
	}

	got, err := s.GetGuardrailRule(r.ID)
	if err != nil {
		t.Fatalf("GetGuardrailRule: %v", err)
	}
	if got.Name != "block-injection" || got.Type != model.GuardrailRegexBlock {
		t.Errorf("name/type: got %q/%q", got.Name, got.Type)
	}
	if got.Hook != model.GuardrailHookInput || got.OnFailure != model.GuardrailActionDeny {
		t.Errorf("hook/action: got %q/%q", got.Hook, got.OnFailure)
	}
	if got.Config != `{"patterns":["ignore previous"]}` {
		t.Errorf("config: got %q", got.Config)
	}
	if got.Priority != 1 || !got.Enabled {
		t.Errorf("priority/enabled: got %d/%v", got.Priority, got.Enabled)
	}

	got.Name = "block-jailbreak"
	got.Type = model.GuardrailBlockedWords
	got.Hook = model.GuardrailHookOutput
	got.OnFailure = model.GuardrailActionFlag
	got.Priority = 2
	got.Enabled = false
	if err := s.UpdateGuardrailRule(got); err != nil {
		t.Fatalf("UpdateGuardrailRule: %v", err)
	}

	updated, err := s.GetGuardrailRule(r.ID)
	if err != nil {
		t.Fatalf("GetGuardrailRule after update: %v", err)
	}
	if updated.Name != "block-jailbreak" || updated.Type != model.GuardrailBlockedWords {
		t.Errorf("updated name/type: got %q/%q", updated.Name, updated.Type)
	}
	if updated.Hook != model.GuardrailHookOutput || updated.OnFailure != model.GuardrailActionFlag {
		t.Errorf("updated hook/action: got %q/%q", updated.Hook, updated.OnFailure)
	}
	if updated.Priority != 2 || updated.Enabled {
		t.Errorf("updated priority/enabled: got %d/%v", updated.Priority, updated.Enabled)
	}

	rules, err := s.GetGuardrailRules()
	if err != nil {
		t.Fatalf("GetGuardrailRules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}

	if err := s.DeleteGuardrailRule(r.ID); err != nil {
		t.Fatalf("DeleteGuardrailRule: %v", err)
	}
	if _, err := s.GetGuardrailRule(r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
	rules, _ = s.GetGuardrailRules()
	if len(rules) != 0 {
		t.Fatalf("expected 0 rules after delete, got %d", len(rules))
	}
}

func TestGuardrails_GetEnabledFilter(t *testing.T) {
	s := openTemp(t)
	s.CreateGuardrailRule(&model.GuardrailRule{
		Name: "a", Type: model.GuardrailRegexBlock, Hook: model.GuardrailHookInput,
		OnFailure: model.GuardrailActionDeny, Enabled: true, Priority: 1,
	})
	s.CreateGuardrailRule(&model.GuardrailRule{
		Name: "b", Type: model.GuardrailBlockedWords, Hook: model.GuardrailHookOutput,
		OnFailure: model.GuardrailActionFlag, Enabled: true, Priority: 2,
	})
	s.CreateGuardrailRule(&model.GuardrailRule{
		Name: "c", Type: model.GuardrailContentLength, Hook: model.GuardrailHookBoth,
		OnFailure: model.GuardrailActionDeny, Enabled: false, Priority: 3,
	})

	enabled, err := s.GetEnabledGuardrailRules()
	if err != nil {
		t.Fatalf("GetEnabledGuardrailRules: %v", err)
	}
	if len(enabled) != 2 {
		t.Fatalf("expected 2 enabled rules, got %d", len(enabled))
	}
	for _, r := range enabled {
		if !r.Enabled {
			t.Errorf("rule %q should be enabled", r.Name)
		}
	}

	all, err := s.GetGuardrailRules()
	if err != nil {
		t.Fatalf("GetGuardrailRules: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 total rules, got %d", len(all))
	}
}

func TestGuardrails_GetRule_NotFound(t *testing.T) {
	s := openTemp(t)
	_, err := s.GetGuardrailRule(999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGuardrails_DeleteRule_NotFound(t *testing.T) {
	s := openTemp(t)
	if err := s.DeleteGuardrailRule(999); err != nil {
		t.Errorf("delete non-existent should not error, got %v", err)
	}
}

func TestGuardrails_PriorityOrdering(t *testing.T) {
	s := openTemp(t)
	s.CreateGuardrailRule(&model.GuardrailRule{
		Name: "low", Type: model.GuardrailRegexBlock, Hook: model.GuardrailHookInput,
		OnFailure: model.GuardrailActionDeny, Enabled: true, Priority: 3,
	})
	s.CreateGuardrailRule(&model.GuardrailRule{
		Name: "high", Type: model.GuardrailRegexBlock, Hook: model.GuardrailHookInput,
		OnFailure: model.GuardrailActionDeny, Enabled: true, Priority: 1,
	})
	s.CreateGuardrailRule(&model.GuardrailRule{
		Name: "mid", Type: model.GuardrailRegexBlock, Hook: model.GuardrailHookInput,
		OnFailure: model.GuardrailActionDeny, Enabled: true, Priority: 2,
	})

	rules, err := s.GetGuardrailRules()
	if err != nil {
		t.Fatalf("GetGuardrailRules: %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(rules))
	}
	if rules[0].Priority != 1 || rules[1].Priority != 2 || rules[2].Priority != 3 {
		t.Errorf("expected priority order 1,2,3; got %d,%d,%d",
			rules[0].Priority, rules[1].Priority, rules[2].Priority)
	}
	if rules[0].Name != "high" || rules[1].Name != "mid" || rules[2].Name != "low" {
		t.Errorf("expected name order high,mid,low; got %s,%s,%s",
			rules[0].Name, rules[1].Name, rules[2].Name)
	}
}

func TestGuardrails_ConfigRoundTrip(t *testing.T) {
	s := openTemp(t)
	configJSON := `{"patterns":["a.*b","^test$"],"max_length":100,"action":"log"}`
	r := &model.GuardrailRule{
		Name: "complex", Type: model.GuardrailRegexBlock, Hook: model.GuardrailHookInput,
		OnFailure: model.GuardrailActionDeny, Config: configJSON, Enabled: true, Priority: 1,
	}
	if err := s.CreateGuardrailRule(r); err != nil {
		t.Fatalf("CreateGuardrailRule: %v", err)
	}
	got, err := s.GetGuardrailRule(r.ID)
	if err != nil {
		t.Fatalf("GetGuardrailRule: %v", err)
	}
	if got.Config != configJSON {
		t.Errorf("config round-trip:\n  got  %q\n  want %q", got.Config, configJSON)
	}
}

func TestGuardrails_AllTypesAndHooks(t *testing.T) {
	s := openTemp(t)
	types := []model.GuardrailType{
		model.GuardrailRegexBlock,
		model.GuardrailBlockedWords,
		model.GuardrailContentLength,
	}
	hooks := []model.GuardrailHook{
		model.GuardrailHookInput,
		model.GuardrailHookOutput,
		model.GuardrailHookBoth,
	}
	actions := []model.GuardrailAction{
		model.GuardrailActionDeny,
		model.GuardrailActionFlag,
	}

	id := int64(0)
	for _, tp := range types {
		for _, hk := range hooks {
			for _, act := range actions {
				r := &model.GuardrailRule{
					Name:      string(tp) + "/" + string(hk) + "/" + string(act),
					Type:      tp,
					Hook:      hk,
					OnFailure: act,
					Config:    "{}",
					Priority:  1,
					Enabled:   true,
				}
				if err := s.CreateGuardrailRule(r); err != nil {
					t.Fatalf("Create(%s/%s/%s): %v", tp, hk, act, err)
				}
				if r.ID <= id {
					t.Fatalf("ID not increasing: %d <= %d", r.ID, id)
				}
				id = r.ID
			}
		}
	}

	rules, err := s.GetGuardrailRules()
	if err != nil {
		t.Fatalf("GetGuardrailRules: %v", err)
	}
	expected := len(types) * len(hooks) * len(actions) // 3*3*2 = 18
	if len(rules) != expected {
		t.Fatalf("expected %d rules, got %d", expected, len(rules))
	}
}

// ──────────────────────────────────────────────────────────
// GuardrailEvents
// ──────────────────────────────────────────────────────────

func TestGuardrailEvents_CRUD(t *testing.T) {
	s := openTemp(t)
	evt := &model.GuardrailEvent{
		TokenID:   1,
		RuleID:    10,
		RuleName:  "block-injection",
		RuleType:  string(model.GuardrailRegexBlock),
		Hook:      string(model.GuardrailHookInput),
		Verdict:   false,
		Action:    string(model.GuardrailActionDeny),
		Detail:    "matched pattern: ignore previous",
		RequestIP: "10.0.0.1",
	}
	if err := s.CreateGuardrailEvent(evt); err != nil {
		t.Fatalf("CreateGuardrailEvent: %v", err)
	}
	if evt.ID == 0 {
		t.Fatal("expected non-zero ID")
	}
	if evt.CreatedAt.IsZero() {
		t.Fatal("created_at should be set")
	}

	events, err := s.GetGuardrailEvents(1, 10)
	if err != nil {
		t.Fatalf("GetGuardrailEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	got := events[0]
	if got.TokenID != 1 || got.RuleID != 10 {
		t.Errorf("token_id/rule_id: got %d/%d", got.TokenID, got.RuleID)
	}
	if got.RuleName != "block-injection" {
		t.Errorf("rule_name: got %q", got.RuleName)
	}
	if got.Verdict {
		t.Error("verdict should be false (blocked)")
	}
	if got.Action != string(model.GuardrailActionDeny) {
		t.Errorf("action: got %q", got.Action)
	}
	if got.RequestIP != "10.0.0.1" {
		t.Errorf("request_ip: got %q", got.RequestIP)
	}
}

func TestGuardrailEvents_OrderDesc(t *testing.T) {
	s := openTemp(t)
	for i := 0; i < 3; i++ {
		evt := &model.GuardrailEvent{
			TokenID:  1,
			RuleID:   1,
			RuleName: "r",
			RuleType: "regex_block",
			Hook:     "input",
			Verdict:  true,
			Action:   "flag",
		}
		if err := s.CreateGuardrailEvent(evt); err != nil {
			t.Fatalf("CreateGuardrailEvent %d: %v", i, err)
		}
	}

	events, err := s.GetGuardrailEvents(1, 10)
	if err != nil {
		t.Fatalf("GetGuardrailEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	for i := 0; i < len(events)-1; i++ {
		if events[i].CreatedAt.Before(events[i+1].CreatedAt) {
			t.Errorf("events not in DESC order: [%d] %v < [%d] %v",
				i, events[i].CreatedAt, i+1, events[i+1].CreatedAt)
		}
	}
}

func TestGuardrailEvents_LimitZero(t *testing.T) {
	s := openTemp(t)
	for i := 0; i < 5; i++ {
		s.CreateGuardrailEvent(&model.GuardrailEvent{
			TokenID: 1, RuleID: 1, RuleName: "r", RuleType: "regex_block",
			Hook: "input", Verdict: true, Action: "flag",
		})
	}
	events, err := s.GetGuardrailEvents(1, 0)
	if err != nil {
		t.Fatalf("GetGuardrailEvents(0): %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("limit=0 should default to 100, got %d events", len(events))
	}
}

func TestGuardrailEvents_LimitOne(t *testing.T) {
	s := openTemp(t)
	for i := 0; i < 3; i++ {
		s.CreateGuardrailEvent(&model.GuardrailEvent{
			TokenID: 1, RuleID: 1, RuleName: "r", RuleType: "regex_block",
			Hook: "input", Verdict: true, Action: "flag",
		})
	}
	events, err := s.GetGuardrailEvents(1, 1)
	if err != nil {
		t.Fatalf("GetGuardrailEvents(1): %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event with limit=1, got %d", len(events))
	}
}

func TestGuardrailEvents_EmptyToken(t *testing.T) {
	s := openTemp(t)
	events, err := s.GetGuardrailEvents(999, 10)
	if err != nil {
		t.Fatalf("GetGuardrailEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(events))
	}
}

// ──────────────────────────────────────────────────────────
// ProviderDefs CRUD
// ──────────────────────────────────────────────────────────

func TestProviderDefs_CRUD(t *testing.T) {
	s := openTemp(t)
	p := &model.ProviderDef{
		Name:        "deepseek",
		DisplayName: "DeepSeek Official",
		Protocol:    "openai",
		BaseURL:     "https://api.deepseek.com/v1",
	}
	if err := s.CreateProviderDef(p); err != nil {
		t.Fatalf("CreateProviderDef: %v", err)
	}
	if p.ID == 0 {
		t.Fatal("expected non-zero ID")
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
		t.Fatal("timestamps should be set")
	}

	defs, err := s.GetProviderDefs()
	if err != nil {
		t.Fatalf("GetProviderDefs: %v", err)
	}
	if len(defs) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(defs))
	}
	if defs[0].Name != "deepseek" || defs[0].Protocol != "openai" {
		t.Errorf("provider mismatch: %+v", defs[0])
	}

	if err := s.DeleteProviderDef(p.ID); err != nil {
		t.Fatalf("DeleteProviderDef: %v", err)
	}
	defs, _ = s.GetProviderDefs()
	if len(defs) != 0 {
		t.Fatalf("expected 0 providers after delete, got %d", len(defs))
	}
}

func TestProviderDefs_DuplicateName(t *testing.T) {
	s := openTemp(t)
	s.CreateProviderDef(&model.ProviderDef{
		Name: "openai", DisplayName: "OpenAI", Protocol: "openai", BaseURL: "https://api.openai.com/v1",
	})
	err := s.CreateProviderDef(&model.ProviderDef{
		Name: "openai", DisplayName: "OpenAI Duplicate", Protocol: "openai", BaseURL: "https://api.openai.com/v1",
	})
	if err == nil {
		t.Fatal("expected error for duplicate provider name")
	}
}

func TestProviderDefs_DeleteNonExistent(t *testing.T) {
	s := openTemp(t)
	if err := s.DeleteProviderDef(999); err != nil {
		t.Errorf("delete non-existent should not error, got %v", err)
	}
}

func TestProviderDefs_FieldsRoundTrip(t *testing.T) {
	s := openTemp(t)
	p := &model.ProviderDef{
		Name:        "anthropic",
		DisplayName: "Anthropic Official",
		Protocol:    "anthropic",
		BaseURL:     "https://api.anthropic.com/v1",
	}
	if err := s.CreateProviderDef(p); err != nil {
		t.Fatalf("CreateProviderDef: %v", err)
	}

	defs, err := s.GetProviderDefs()
	if err != nil {
		t.Fatalf("GetProviderDefs: %v", err)
	}
	got := defs[0]
	if got.Name != "anthropic" {
		t.Errorf("name: got %q", got.Name)
	}
	if got.DisplayName != "Anthropic Official" {
		t.Errorf("display_name: got %q", got.DisplayName)
	}
	if got.Protocol != "anthropic" {
		t.Errorf("protocol: got %q", got.Protocol)
	}
	if got.BaseURL != "https://api.anthropic.com/v1" {
		t.Errorf("base_url: got %q", got.BaseURL)
	}
}

// ──────────────────────────────────────────────────────────
// GetDrainedChannels
// ──────────────────────────────────────────────────────────

func TestGetDrainedChannels_ActiveKeyNotDrained(t *testing.T) {
	s := openTemp(t)
	ch := &model.Channel{
		Name: "openai", Provider: "openai", BaseURL: "https://api.openai.com/v1",
		Models: []string{"gpt-4o"}, Status: model.ChannelEnabled,
	}
	s.CreateChannel(ch)
	s.CreateKey(&model.Key{ChannelID: ch.ID, Key: "sk-1", KeyMasked: "sk-***", Status: model.KeyActive})

	drained, err := s.GetDrainedChannels()
	if err != nil {
		t.Fatalf("GetDrainedChannels: %v", err)
	}
	if len(drained) != 0 {
		t.Fatalf("expected 0 drained channels, got %d", len(drained))
	}
}

func TestGetDrainedChannels_NoKeysIsDrained(t *testing.T) {
	s := openTemp(t)
	ch := &model.Channel{
		Name: "deepseek", Provider: "deepseek", BaseURL: "https://api.deepseek.com/v1",
		Models: []string{"deepseek-chat"}, Status: model.ChannelEnabled,
	}
	s.CreateChannel(ch)

	drained, err := s.GetDrainedChannels()
	if err != nil {
		t.Fatalf("GetDrainedChannels: %v", err)
	}
	if len(drained) != 1 {
		t.Fatalf("expected 1 drained channel, got %d", len(drained))
	}
	if drained[0].ID != ch.ID || drained[0].Name != "deepseek" {
		t.Errorf("drained channel mismatch: %+v", drained[0])
	}
}

func TestGetDrainedChannels_DeleteAllKeysDrains(t *testing.T) {
	s := openTemp(t)
	ch := &model.Channel{
		Name: "openai", Provider: "openai", BaseURL: "https://api.openai.com/v1",
		Models: []string{"gpt-4o"}, Status: model.ChannelEnabled,
	}
	s.CreateChannel(ch)
	k1 := &model.Key{ChannelID: ch.ID, Key: "sk-1", KeyMasked: "sk-***", Status: model.KeyActive}
	k2 := &model.Key{ChannelID: ch.ID, Key: "sk-2", KeyMasked: "sk-***", Status: model.KeyActive}
	s.CreateKey(k1)
	s.CreateKey(k2)

	drained, _ := s.GetDrainedChannels()
	if len(drained) != 0 {
		t.Fatalf("with 2 active keys, expected 0 drained, got %d", len(drained))
	}

	s.DeleteKey(k1.ID)
	s.DeleteKey(k2.ID)

	drained, err := s.GetDrainedChannels()
	if err != nil {
		t.Fatalf("GetDrainedChannels: %v", err)
	}
	if len(drained) != 1 {
		t.Fatalf("after deleting all keys, expected 1 drained, got %d", len(drained))
	}
}

func TestGetDrainedChannels_PartialDeleteNotDrained(t *testing.T) {
	s := openTemp(t)
	ch := &model.Channel{
		Name: "openai", Provider: "openai", BaseURL: "https://api.openai.com/v1",
		Models: []string{"gpt-4o"}, Status: model.ChannelEnabled,
	}
	s.CreateChannel(ch)
	k1 := &model.Key{ChannelID: ch.ID, Key: "sk-1", KeyMasked: "sk-***", Status: model.KeyActive}
	k2 := &model.Key{ChannelID: ch.ID, Key: "sk-2", KeyMasked: "sk-***", Status: model.KeyActive}
	s.CreateKey(k1)
	s.CreateKey(k2)

	s.DeleteKey(k1.ID)

	drained, err := s.GetDrainedChannels()
	if err != nil {
		t.Fatalf("GetDrainedChannels: %v", err)
	}
	if len(drained) != 0 {
		t.Fatalf("with 1 remaining key, expected 0 drained, got %d", len(drained))
	}
}

func TestGetDrainedChannels_DisabledChannelNotReturned(t *testing.T) {
	s := openTemp(t)
	ch := &model.Channel{
		Name: "disabled", Provider: "x", BaseURL: "https://x.com",
		Models: []string{"m"}, Status: model.ChannelDisabled,
	}
	s.CreateChannel(ch)

	drained, err := s.GetDrainedChannels()
	if err != nil {
		t.Fatalf("GetDrainedChannels: %v", err)
	}
	if len(drained) != 0 {
		t.Fatalf("disabled channel should not be drained, got %d", len(drained))
	}
}

func TestGetDrainedChannels_EmptyTable(t *testing.T) {
	s := openTemp(t)
	drained, err := s.GetDrainedChannels()
	if err != nil {
		t.Fatalf("GetDrainedChannels: %v", err)
	}
	if len(drained) != 0 {
		t.Fatalf("expected 0 drained channels, got %d", len(drained))
	}
}
