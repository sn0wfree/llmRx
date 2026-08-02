package webui

import (
	"net/http"

	"github.com/sn0wfree/llmRx/internal/model"
)

// GuardrailsPage renders the guardrail rules management page.
func (h *Handler) GuardrailsPage(w http.ResponseWriter, r *http.Request) {
	rules := h.loadGuardrailRules()
	data := map[string]any{
		"Body":   "guardrails_list_body",
		"Title":  "安全规则",
		"User":   userToView(getUser(r)),
		"Active": "guardrails",
		"Rules":  rules,
	}
	if err := h.renderer.Render(w, "guardrails_list_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) loadGuardrailRules() []model.GuardrailRule {
	storeRules, err := h.store.GetGuardrailRules()
	if err != nil || storeRules == nil {
		return []model.GuardrailRule{}
	}
	return storeRules
}

// GuardrailRuleCreate handles the form submission to add a guardrail rule.
func (h *Handler) GuardrailRuleCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rule := model.GuardrailRule{
		Name:        r.FormValue("name"),
		Description: r.FormValue("description"),
		Type:        model.GuardrailType(r.FormValue("type")),
		Hook:        model.GuardrailHook(r.FormValue("hook")),
		OnFailure:   model.GuardrailAction(r.FormValue("on_failure")),
		Enabled:     r.FormValue("enabled") == "on",
	}
	config := r.FormValue("config")
	if config == "" {
		config = "{}"
	}
	rule.Config = config
	if rule.Name == "" || rule.Type == "" || rule.Hook == "" {
		http.Error(w, "name, type, and hook are required", http.StatusBadRequest)
		return
	}
	if err := h.store.CreateGuardrailRule(&rule); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/guardrails", http.StatusSeeOther)
}

// GuardrailRuleDelete handles deleting a guardrail rule.
func (h *Handler) GuardrailRuleDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	idStr := r.FormValue("id")
	if idStr == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	var id int64
	for _, c := range idStr {
		if c >= '0' && c <= '9' {
			id = id*10 + int64(c-'0')
		}
	}
	if err := h.store.DeleteGuardrailRule(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/guardrails", http.StatusSeeOther)
}
