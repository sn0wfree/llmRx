package webui

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/model"
)

// TokensPage renders the tokens list.
func (h *Handler) TokensPage(w http.ResponseWriter, r *http.Request) {
	h.tokensListPage(w, r, "")
}

// TokensListPartial returns the table body for HTMX search.
func (h *Handler) TokensListPartial(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	h.tokensListPage(w, r, q)
}

func (h *Handler) tokensListPage(w http.ResponseWriter, r *http.Request, query string) {
	toks, err := h.store.GetTokens()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if query != "" {
		filtered := toks[:0]
		for _, t := range toks {
			if strings.Contains(strings.ToLower(t.Name), strings.ToLower(query)) {
				filtered = append(filtered, t)
			}
		}
		toks = filtered
	}

	if strings.HasPrefix(r.URL.Path, "/partial/") {
		if err := h.renderer.RenderPartial(w, "tokens_table_body", map[string]any{
			"Tokens": toks,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	data := map[string]any{
		"Body":   "tokens_list_body",
		"Title":  "Token 管理",
		"User":   userToView(getUser(r)),
		"Active": "tokens",
		"Tokens": toks,
	}
	if err := h.renderer.Render(w, "tokens_list_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// TokenNewForm renders the new token form.
func (h *Handler) TokenNewForm(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Body":   "tokens_form_body",
		"Title":  "新建 Token",
		"User":   userToView(getUser(r)),
		"Active": "tokens",
	}
	if err := h.renderer.Render(w, "tokens_form_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// TokenEditForm renders the edit form.
func (h *Handler) TokenEditForm(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	t, err := h.store.GetTokenByID(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	data := map[string]any{
		"Body":   "tokens_form_body",
		"Title":  "编辑 Token",
		"User":   userToView(getUser(r)),
		"Active": "tokens",
		"Token":  t,
	}
	if err := h.renderer.Render(w, "tokens_form_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// TokenCreate handles form POST to create a new token.
func (h *Handler) TokenCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderTokenFormError(w, r, nil, "表单解析失败", nil)
		return
	}
	plain := newSessionToken()
	t := &model.Token{
		Key:             plain,
		Name:            strings.TrimSpace(r.FormValue("name")),
		PlanID:          parseInt64Default(r.FormValue("plan_id"), 0),
		Status:          model.TokenActive,
		RPM:             parseIntDefault(r.FormValue("rpm"), 0),
		TPM:             parseIntDefault(r.FormValue("tpm"), 0),
		ModelsWhitelist: splitLines(r.FormValue("models_whitelist")),
		IPWhitelist:     splitLines(r.FormValue("ip_whitelist")),
		CreatedAt:       time.Now(),
	}
	if r.FormValue("status") != "1" {
		t.Status = model.TokenDisabled
	}
	if days := parseIntDefault(r.FormValue("expires_in_days"), 0); days > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(days) * 24 * time.Hour)
	}
	if err := h.store.CreateToken(t); err != nil {
		h.renderTokenFormError(w, r, nil, "创建失败: "+err.Error(), r.Form)
		return
	}
	h.triggerReload()
	// Show the new key once
	data := map[string]any{
		"Body":   "tokens_form_body",
		"Title":  "新建 Token",
		"User":   userToView(getUser(r)),
		"Active": "tokens",
		"NewKey": plain,
		"Token":  t,
	}
	if err := h.renderer.Render(w, "tokens_form_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// TokenAction dispatches POST /tokens/{id} to update/delete based on _method.
func (h *Handler) TokenAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch strings.ToUpper(r.FormValue("_method")) {
	case "PUT":
		h.updateTokenByID(w, r, id)
	case "DELETE":
		h.deleteTokenByID(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// TokenUpdate is kept for API compat (HTMX PUT).
func (h *Handler) TokenUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	h.updateTokenByID(w, r, id)
}

func (h *Handler) updateTokenByID(w http.ResponseWriter, r *http.Request, id int64) {
	cur, err := h.store.GetTokenByID(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderTokenFormError(w, r, cur, "表单解析失败", nil)
		return
	}
	if v := strings.TrimSpace(r.FormValue("name")); v != "" {
		cur.Name = v
	}
	if v := r.FormValue("plan_id"); v != "" {
		cur.PlanID = parseInt64Default(v, cur.PlanID)
	}
	cur.RPM = parseIntDefault(r.FormValue("rpm"), cur.RPM)
	cur.TPM = parseIntDefault(r.FormValue("tpm"), cur.TPM)
	if v := r.FormValue("models_whitelist"); v != "" {
		cur.ModelsWhitelist = splitLines(v)
	}
	if v := r.FormValue("ip_whitelist"); v != "" {
		cur.IPWhitelist = splitLines(v)
	}
	cur.Status = model.TokenDisabled
	if r.FormValue("status") == "1" {
		cur.Status = model.TokenActive
	}
	if days := parseIntDefault(r.FormValue("expires_in_days"), -1); days >= 0 {
		if days > 0 {
			cur.ExpiresAt = time.Now().Add(time.Duration(days) * 24 * time.Hour)
		} else {
			cur.ExpiresAt = time.Time{}
		}
	}
	if err := h.store.UpdateToken(cur); err != nil {
		h.renderTokenFormError(w, r, cur, "更新失败: "+err.Error(), r.Form)
		return
	}
	h.triggerReload()
	http.Redirect(w, r, "/admin/tokens", http.StatusSeeOther)
}

// TokenDelete handles DELETE for a token.
func (h *Handler) TokenDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	h.deleteTokenByID(w, r, id)
}

func (h *Handler) deleteTokenByID(w http.ResponseWriter, r *http.Request, id int64) {
	if err := h.store.DeleteToken(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.triggerReload()
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) renderTokenFormError(w http.ResponseWriter, r *http.Request, t *model.Token, msg string, form map[string][]string) {
	fd := map[string]string{}
	if form != nil {
		fd["Name"] = firstOrEmpty(form["name"])
		fd["PlanIDStr"] = firstOrEmpty(form["plan_id"])
		fd["RPMStr"] = firstOrEmpty(form["rpm"])
		fd["TPMStr"] = firstOrEmpty(form["tpm"])
		fd["ModelsStr"] = firstOrEmpty(form["models_whitelist"])
		fd["IPsStr"] = firstOrEmpty(form["ip_whitelist"])
		fd["ExpiresDaysStr"] = firstOrEmpty(form["expires_in_days"])
		fd["Status"] = firstOrEmpty(form["status"])
	}
	data := map[string]any{
		"Body":      "tokens_form_body",
		"Title":     "Token 表单",
		"User":      userToView(getUser(r)),
		"Active":    "tokens",
		"Token":     t,
		"FormError": msg,
		"FormData":  fd,
	}
	if err := h.renderer.Render(w, "tokens_form_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func parseInt64Default(s string, def int64) int64 {
	if s == "" {
		return def
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return v
}

// Note: `strings` and `time` already imported. Use existing helpers.
func containsArr(slice []string, s string) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}
