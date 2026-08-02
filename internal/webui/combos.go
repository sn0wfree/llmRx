package webui

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/model"
)

// CombosPage renders the list of combos for a given token.
func (h *Handler) CombosPage(w http.ResponseWriter, r *http.Request) {
	tokenID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	token, err := h.store.GetTokenByID(tokenID)
	if err != nil {
		http.Error(w, "token not found", http.StatusNotFound)
		return
	}
	combos, err := h.store.GetComboModels(tokenID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Body":   "combos_list_body",
		"Title":  "组合模型",
		"User":   userToView(getUser(r)),
		"Active": "tokens",
		"Token":  token,
		"Combos": combos,
	}
	if err := h.renderer.Render(w, "combos_list_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ComboNewForm renders the form to create a new combo for a token.
func (h *Handler) ComboNewForm(w http.ResponseWriter, r *http.Request) {
	tokenID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	token, err := h.store.GetTokenByID(tokenID)
	if err != nil {
		http.Error(w, "token not found", http.StatusNotFound)
		return
	}
	data := map[string]any{
		"Body":        "combo_form_body",
		"Title":       "新建组合模型",
		"User":        userToView(getUser(r)),
		"Active":      "tokens",
		"Token":       token,
		"TiersJSON":   "",
		"FallbackStr": "",
	}
	if err := h.renderer.Render(w, "combo_form_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ComboEditForm renders the form to edit an existing combo.
func (h *Handler) ComboEditForm(w http.ResponseWriter, r *http.Request) {
	tokenID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	comboID, err := strconv.ParseInt(chi.URLParam(r, "cid"), 10, 64)
	if err != nil {
		http.Error(w, "bad combo id", http.StatusBadRequest)
		return
	}
	token, err := h.store.GetTokenByID(tokenID)
	if err != nil {
		http.Error(w, "token not found", http.StatusNotFound)
		return
	}
	combo, err := h.store.GetComboModel(comboID)
	if err != nil {
		http.Error(w, "combo not found", http.StatusNotFound)
		return
	}
	data := map[string]any{
		"Body":        "combo_form_body",
		"Title":       "编辑组合模型",
		"User":        userToView(getUser(r)),
		"Active":      "tokens",
		"Token":       token,
		"Combo":       combo,
		"TiersJSON":   tiersJSON(combo.Tiers),
		"FallbackStr": strings.Join(combo.Fallback, "\n"),
	}
	if err := h.renderer.Render(w, "combo_form_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// comboFormFields is the canonical field list for combo forms.
var comboFormFields = []string{"name", "mode", "models", "tiers", "fallback", "strategy", "status"}

// parseComboTiers decodes the tiers JSON textarea. Empty input
// yields nil (no tiers). A malformed document is an error so the
// form can surface it instead of silently creating an auto combo
// with no candidates.
func parseComboTiers(raw string) (map[string]model.TierConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var t map[string]model.TierConfig
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		return nil, err
	}
	return t, nil
}

// tiersJSON renders a combo's tier table back into the form's JSON
// textarea (pretty-printed for operators).
func tiersJSON(t map[string]model.TierConfig) string {
	if len(t) == 0 {
		return ""
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

// ComboCreate handles form POST to create a new combo.
func (h *Handler) ComboCreate(w http.ResponseWriter, r *http.Request) {
	tokenID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderComboFormError(w, r, tokenID, nil, "表单解析失败", r.Form, nil)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	modelsRaw := strings.TrimSpace(r.FormValue("models"))
	mode := r.FormValue("mode")
	strategy := r.FormValue("strategy")
	tiersRaw := strings.TrimSpace(r.FormValue("tiers"))
	fallbackRaw := strings.TrimSpace(r.FormValue("fallback"))
	enabled := r.FormValue("status") == "1"

	fieldErrors := map[string]string{}
	if name == "" {
		fieldErrors["name"] = "虚拟模型名必填"
	} else if !isValidComboName(name) {
		fieldErrors["name"] = "仅允许字母数字下划线连字符，≤64 字符"
	}
	if mode == "auto" {
		if tiersRaw == "" {
			fieldErrors["tiers"] = "auto 模式需要复杂度分档配置"
		} else if _, err := parseComboTiers(tiersRaw); err != nil {
			fieldErrors["tiers"] = "tiers JSON 解析失败: " + err.Error()
		}
	} else if modelsRaw == "" {
		fieldErrors["models"] = "至少需要一个底层模型"
	}
	if len(fieldErrors) > 0 {
		h.renderComboFormError(w, r, tokenID, nil, "请修正表单错误", r.Form, fieldErrors)
		return
	}
	var models []string
	if modelsRaw != "" {
		models = splitLines(modelsRaw)
		if len(models) == 0 {
			fieldErrors["models"] = "模型列表解析为空"
			h.renderComboFormError(w, r, tokenID, nil, "请修正表单错误", r.Form, fieldErrors)
			return
		}
	}
	if mode == "" {
		mode = "load_balance"
	}
	tiers, _ := parseComboTiers(tiersRaw)
	fallback := splitLines(fallbackRaw)
	c := &model.TokenComboModel{
		TokenID:  tokenID,
		Name:     name,
		Models:   models,
		Mode:     model.ComboMode(mode),
		Strategy: model.CostStrategy(strategy),
		Tiers:    tiers,
		Fallback: fallback,
		Enabled:  enabled,
	}
	if err := h.store.CreateComboModel(c); err != nil {
		h.renderComboFormError(w, r, tokenID, nil, "创建失败: "+err.Error(), r.Form, nil)
		return
	}
	logging.Info("combo.created",
		logging.F("token_id", tokenID),
		logging.F("combo_id", c.ID),
		logging.F("name", name),
		logging.F("mode", mode),
		logging.F("models_count", len(models)),
	)
	h.triggerReload()
	http.Redirect(w, r, "/admin/tokens/"+strconv.FormatInt(tokenID, 10)+"/combos", http.StatusSeeOther)
}

// renderComboFormError is the combo-specific error renderer. It
// delegates to the shared renderFormError but adds the token
// lookup so the back-link works.
func (h *Handler) renderComboFormError(w http.ResponseWriter, r *http.Request, tokenID int64, combo *model.TokenComboModel, msg string, form map[string][]string, fieldErrors map[string]string) {
	token, _ := h.store.GetTokenByID(tokenID)
	if token != nil {
		// Build a tiny formData map so the template can echo
		// back the user's input on retry.
		fd := map[string]string{}
		if form != nil {
			fd["Name"] = firstOrEmpty(form["name"])
			fd["Mode"] = firstOrEmpty(form["mode"])
			fd["ModelsStr"] = firstOrEmpty(form["models"])
			fd["TiersStr"] = firstOrEmpty(form["tiers"])
			fd["FallbackStr"] = firstOrEmpty(form["fallback"])
			fd["Strategy"] = firstOrEmpty(form["strategy"])
			fd["Status"] = firstOrEmpty(form["status"])
		}
		data := map[string]any{
			"Body":        "combo_form_body",
			"Title":       "组合模型表单",
			"User":        userToView(getUser(r)),
			"Active":      "tokens",
			"Token":       token,
			"Combo":       combo,
			"FormError":   msg,
			"FormData":    fd,
			"TiersJSON":   fd["TiersStr"],
			"FallbackStr": fd["FallbackStr"],
			"FieldErrors": fieldErrors,
		}
		if err := h.renderer.Render(w, "combo_form_body", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	http.Error(w, msg, http.StatusBadRequest)
}

// isValidComboName mirrors the regex documented on the combo form:
// `^[a-zA-Z0-9_-]{1,64}$`. Kept here (not in model) because the
// model layer doesn't care — this is a UI affordance.
func isValidComboName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// ComboAction dispatches POST /tokens/{id}/combos/{cid} to update or
// delete based on the hidden _method field.
func (h *Handler) ComboAction(w http.ResponseWriter, r *http.Request) {
	tokenID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	comboID, err := strconv.ParseInt(chi.URLParam(r, "cid"), 10, 64)
	if err != nil {
		http.Error(w, "bad combo id", http.StatusBadRequest)
		return
	}
	method, ok := formMethod(w, r, true) // combo forms default to PUT
	if !ok {
		return
	}
	switch method {
	case "PUT":
		h.comboUpdateByID(w, r, tokenID, comboID)
	case "DELETE":
		h.comboDeleteByID(w, r, tokenID, comboID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) comboUpdateByID(w http.ResponseWriter, r *http.Request, tokenID, comboID int64) {
	existing, err := h.store.GetComboModel(comboID)
	if err != nil {
		http.Error(w, "combo not found", http.StatusNotFound)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	modelsRaw := strings.TrimSpace(r.FormValue("models"))
	mode := r.FormValue("mode")
	strategy := r.FormValue("strategy")
	tiersRaw := strings.TrimSpace(r.FormValue("tiers"))
	fallbackRaw := strings.TrimSpace(r.FormValue("fallback"))
	enabled := r.FormValue("status") == "1"

	fieldErrors := map[string]string{}
	if name != "" && !isValidComboName(name) {
		fieldErrors["name"] = "仅允许字母数字下划线连字符，≤64 字符"
	}
	if mode == "auto" {
		if tiersRaw == "" {
			fieldErrors["tiers"] = "auto 模式需要复杂度分档配置"
		} else if _, err := parseComboTiers(tiersRaw); err != nil {
			fieldErrors["tiers"] = "tiers JSON 解析失败: " + err.Error()
		}
	} else if modelsRaw != "" && len(splitLines(modelsRaw)) == 0 {
		fieldErrors["models"] = "模型列表解析为空"
	}
	if len(fieldErrors) > 0 {
		h.renderComboFormError(w, r, tokenID, existing, "请修正表单错误", r.Form, fieldErrors)
		return
	}

	if name != "" {
		existing.Name = name
	}
	if modelsRaw != "" {
		existing.Models = splitLines(modelsRaw)
	}
	if mode != "" {
		existing.Mode = model.ComboMode(mode)
	}
	if tiersRaw != "" || mode == "auto" {
		existing.Tiers, _ = parseComboTiers(tiersRaw)
	}
	if fallbackRaw != "" || mode == "auto" {
		existing.Fallback = splitLines(fallbackRaw)
	}
	existing.Strategy = model.CostStrategy(strategy)
	existing.Enabled = enabled

	if err := h.store.UpdateComboModel(existing); err != nil {
		h.renderComboFormError(w, r, tokenID, existing, "更新失败: "+err.Error(), r.Form, nil)
		return
	}
	logging.Info("combo.updated",
		logging.F("token_id", tokenID),
		logging.F("combo_id", existing.ID),
		logging.F("name", existing.Name),
		logging.F("mode", existing.Mode),
		logging.F("models_count", len(existing.Models)),
	)
	h.triggerReload()
	http.Redirect(w, r, "/admin/tokens/"+strconv.FormatInt(tokenID, 10)+"/combos", http.StatusSeeOther)
}

// ComboSetDefault promotes a combo to be the token's default set
// (the alias "auto" routes through it). Returns the combos list
// partial so HTMX can re-render the table in place.
func (h *Handler) ComboSetDefault(w http.ResponseWriter, r *http.Request) {
	tokenID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	comboID, err := strconv.ParseInt(chi.URLParam(r, "cid"), 10, 64)
	if err != nil {
		http.Error(w, "bad combo id", http.StatusBadRequest)
		return
	}
	if _, err := h.store.GetTokenByID(tokenID); err != nil {
		http.Error(w, "token not found", http.StatusNotFound)
		return
	}
	if err := h.store.SetDefaultModelSet(tokenID, comboID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Info("combo.set_default",
		logging.F("token_id", tokenID),
		logging.F("combo_id", comboID),
	)
	h.triggerReload()
	http.Redirect(w, r, "/admin/tokens/"+strconv.FormatInt(tokenID, 10)+"/combos", http.StatusSeeOther)
}

// ComboDelete handles DELETE for a combo.
func (h *Handler) ComboDelete(w http.ResponseWriter, r *http.Request) {
	tokenID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	comboID, err := strconv.ParseInt(chi.URLParam(r, "cid"), 10, 64)
	if err != nil {
		http.Error(w, "bad combo id", http.StatusBadRequest)
		return
	}
	h.comboDeleteByID(w, r, tokenID, comboID)
}

func (h *Handler) comboDeleteByID(w http.ResponseWriter, _ *http.Request, tokenID, comboID int64) {
	if err := h.store.DeleteComboModel(comboID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Info("combo.deleted",
		logging.F("token_id", tokenID),
		logging.F("combo_id", comboID),
	)
	h.triggerReload()
	w.WriteHeader(http.StatusOK)
}
