package scenarios

import (
	"encoding/json"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestScenario_GuardrailFlagAction(t *testing.T) {
	app := testhelper.New(t)
	app.AddChannel("ch1", "openai", "https://api.openai.com/v1",
		[]string{"gpt-4o"}, "sk-key-1")
	app.AddToken("sk-tok-1", "test-token")

	cfg, _ := json.Marshal(map[string]interface{}{
		"words": []string{"flagged_word"},
	})
	rule := app.AddGuardrailRule("flag-rule", model.GuardrailBlockedWords,
		model.GuardrailHookInput, string(cfg))
	rule.OnFailure = model.GuardrailActionFlag
	if err := app.Store.UpdateGuardrailRule(rule); err != nil {
		t.Fatalf("UpdateGuardrailRule: %v", err)
	}

	code, _ := doChat(t, app, "sk-tok-1", "gpt-4o",
		userMsg("this has flagged_word in it"))
	if code != 200 {
		t.Fatalf("flag action should not block: got %d, want 200", code)
	}
}
