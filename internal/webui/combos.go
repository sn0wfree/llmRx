package webui

import (
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
		"Body":    "combos_list_body",
		"Title":   "组合模型",
		"User":    userToView(getUser(r)),
		"Active":  "tokens",
		"Token":   token,
		"Combos":  combos,
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
		"Body":    "combo_form_body",
		"Title":   "新建组合模型",
		"User":    userToView(getUser(r)),
		"Active":  "tokens",
		"Token":   token,
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
		"Body":    "combo_form_body",
		"Title":   "编辑组合模型",
		"User":    userToView(getUser(r)),
		"Active":  "tokens",
		"Token":   token,
		"Combo":   combo,
	}
	if err := h.renderer.Render(w, "combo_form_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// comboFormFields is the canonical field list for combo forms.
var comboFormFields = []string{"name", "mode", "models", "strategy", "status"}

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
	enabled := r.FormValue("status") == "1"

	fieldErrors := map[string]string{}
	if name == "" {
		fieldErrors["name"] = "虚拟模型名必填"
	} else if !isValidComboName(name) {
		fieldErrors["name"] = "仅允许字母数字下划线连字符，≤64 字符"
	}
	if modelsRaw == "" {
		fieldErrors["models"] = "至少需要一个底层模型"
	}
	if len(fieldErrors) > 0 {
		h.renderComboFormError(w, r, tokenID, nil, "请修正表单错误", r.Form, fieldErrors)
		return
	}
	models := splitLines(modelsRaw)
	if len(models) == 0 {
		fieldErrors["models"] = "模型列表解析为空"
		h.renderComboFormError(w, r, tokenID, nil, "请修正表单错误", r.Form, fieldErrors)
		return
	}
	if mode == "" {
		mode = "load_balance"
	}
	c := &model.TokenComboModel{
		TokenID:  tokenID,
		Name:     name,
		Models:   models,
		Mode:     model.ComboMode(mode),
		Strategy: model.CostStrategy(strategy),
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "form parse error", http.StatusBadRequest)
		return
	}
	method := strings.ToUpper(r.FormValue("_method"))
	if method == "" {
		method = "PUT" // default for form submit
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
	enabled := r.FormValue("status") == "1"

	fieldErrors := map[string]string{}
	if name != "" && !isValidComboName(name) {
		fieldErrors["name"] = "仅允许字母数字下划线连字符，≤64 字符"
	}
	if modelsRaw != "" && len(splitLines(modelsRaw)) == 0 {
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


