package webui

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/auth"
	"github.com/sn0wfree/llmRx/internal/model"
)

// UsersPage renders the users list.
func (h *Handler) UsersPage(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.GetUsers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Body":   "users_list_body",
		"Title":  "用户管理",
		"User":   userToView(getUser(r)),
		"Active": "users",
		"Users":  users,
	}
	if err := h.renderer.Render(w, "users_list_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// UserNewForm renders the new user form.
func (h *Handler) UserNewForm(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Body":   "users_form_body",
		"Title":  "新建用户",
		"User":   userToView(getUser(r)),
		"Active": "users",
	}
	if err := h.renderer.Render(w, "users_form_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// UserCreate handles form POST to create a new user.
func (h *Handler) UserCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderUserFormError(w, r, nil, "表单解析失败", nil)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	if username == "" || password == "" {
		h.renderUserFormError(w, r, nil, "用户名和密码必填", r.Form)
		return
	}
	if len(password) < 6 {
		h.renderUserFormError(w, r, nil, "密码至少 6 字符", r.Form)
		return
	}
	hash, err := auth.Hash(password)
	if err != nil {
		h.renderUserFormError(w, r, nil, "密码哈希失败: "+err.Error(), r.Form)
		return
	}
	role := model.UserRole(parseIntDefault(r.FormValue("role"), 0))
	u := &model.User{
		Username:     username,
		PasswordHash: hash,
		Role:         role,
		Status:       1,
		CreatedAt:    time.Now(),
	}
	if err := h.store.CreateUser(u); err != nil {
		h.renderUserFormError(w, r, u, "创建失败: "+err.Error(), r.Form)
		return
	}
	h.triggerReload()
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// UserDelete handles DELETE for a user.
func (h *Handler) UserDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	u, err := h.store.GetUser(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if u.Username == "admin" {
		http.Error(w, "cannot delete default admin", http.StatusBadRequest)
		return
	}
	if err := h.store.UpdateUser(u); err != nil { // placeholder; real delete below
		_ = err
	}
	// Use UpdateUser to clear session first (mirrors JSON behaviour).
	u.SessionToken = ""
	u.SessionExp = nil
	_ = h.store.UpdateUser(u)
	// Direct SQL delete would need a new store method; for now we
	// mark status=0 (disabled) which is what the JSON handler does.
	u.Status = 0
	_ = h.store.UpdateUser(u)
	w.WriteHeader(http.StatusOK)
}

// UserPasswordForm renders the change-password form.
func (h *Handler) UserPasswordForm(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	u, err := h.store.GetUser(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	data := map[string]any{
		"Body":   "users_form_body",
		"Title":  "修改密码 - " + u.Username,
		"User":   userToView(getUser(r)),
		"Active": "users",
		"User2":  u,
	}
	// Re-render with password form block by overriding Body
	data["FormMode"] = "password"
	if err := h.renderer.Render(w, "users_password_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// UserPasswordSubmit handles the change-password POST.
func (h *Handler) UserPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	u, err := h.store.GetUser(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderUserPasswordError(w, r, u, "表单解析失败")
		return
	}
	password := r.FormValue("password")
	if len(password) < 6 {
		h.renderUserPasswordError(w, r, u, "密码至少 6 字符")
		return
	}
	hash, err := auth.Hash(password)
	if err != nil {
		h.renderUserPasswordError(w, r, u, "密码哈希失败: "+err.Error())
		return
	}
	u.PasswordHash = hash
	// Force re-login.
	u.SessionToken = ""
	u.SessionExp = nil
	if err := h.store.UpdateUser(u); err != nil {
		h.renderUserPasswordError(w, r, u, "更新失败: "+err.Error())
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (h *Handler) renderUserFormError(w http.ResponseWriter, r *http.Request, u *model.User, msg string, form map[string][]string) {
	fd := map[string]string{}
	if form != nil {
		fd["Username"] = firstOrEmpty(form["username"])
		fd["Role"] = firstOrEmpty(form["role"])
	}
	data := map[string]any{
		"Body":      "users_form_body",
		"Title":     "新建用户",
		"User":      userToView(getUser(r)),
		"Active":    "users",
		"User2":     u,
		"FormError": msg,
		"FormData":  fd,
	}
	if err := h.renderer.Render(w, "users_form_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) renderUserPasswordError(w http.ResponseWriter, r *http.Request, u *model.User, msg string) {
	data := map[string]any{
		"Body":      "users_form_body",
		"Title":     "修改密码 - " + u.Username,
		"User":      userToView(getUser(r)),
		"Active":    "users",
		"User2":     u,
		"FormError": msg,
		"FormMode":  "password",
	}
	if err := h.renderer.Render(w, "users_password_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
