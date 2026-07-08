package webui

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/auth"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

// Handler holds dependencies for the admin web UI.
type Handler struct {
	store     store.Store
	renderer  *Renderer
	adminH    *webAPIBridge
}

// New creates a web UI handler. adminAPI provides the legacy
// JSON endpoints under /api/v1/* that the page handlers still call
// (e.g. for reload after writes). The handler routes /admin/* to
// HTML pages backed by embedded templates.
func New(st store.Store, adminAPI *webAPIBridge) (*Handler, error) {
	r, err := NewRenderer()
	if err != nil {
		return nil, err
	}
	return &Handler{store: st, renderer: r, adminH: adminAPI}, nil
}

// Routes returns the http handler that serves /admin/*.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()

	// Public
	r.Get("/login", h.LoginPage)
	r.Post("/login", h.LoginSubmit)

	// Authenticated
	r.Group(func(r chi.Router) {
		r.Use(SessionMiddleware(h.store))

		r.Get("/", func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, "/admin/dashboard", http.StatusSeeOther)
		})
		r.Get("/dashboard", h.DashboardPage)
		r.Post("/logout", h.LogoutSubmit)

		// Channels
		r.Get("/channels", h.ChannelsPage)
		r.Get("/channels/partial/list", h.ChannelsListPartial)
		r.Get("/channels/new", h.ChannelNewForm)
		r.Get("/channels/{id}/edit", h.ChannelEditForm)
		r.Get("/channels/{id}/keys", h.ChannelKeysPage)
		r.Post("/channels", h.ChannelCreate)
		// Form-based edit/delete: browsers only support GET/POST, so
		// we register POST and dispatch on the _method field inside
		// the handler. HTMX uses X-HTTP-Method-Override or sends the
		// real verb via fetch.
		r.Post("/channels/{id}", h.ChannelAction)
		r.Delete("/channels/{id}", h.ChannelDelete)
		r.Post("/channels/{id}/keys", h.ChannelKeyCreate)
		r.Delete("/channels/{id}/keys/{keyId}", h.ChannelKeyDelete)

		// Tokens
		r.Get("/tokens", h.TokensPage)
		r.Get("/tokens/partial/list", h.TokensListPartial)
		r.Get("/tokens/new", h.TokenNewForm)
		r.Get("/tokens/{id}/edit", h.TokenEditForm)
		r.Post("/tokens", h.TokenCreate)
		r.Post("/tokens/{id}", h.TokenAction)
		r.Delete("/tokens/{id}", h.TokenDelete)

		// Plans
		r.Get("/plans", h.PlansPage)
		r.Get("/plans/new", h.PlanNewForm)
		r.Get("/plans/{id}/edit", h.PlanEditForm)
		r.Post("/plans", h.PlanCreate)
		r.Post("/plans/{id}", h.PlanAction)

		// Users
		r.Get("/users", h.UsersPage)
		r.Get("/users/new", h.UserNewForm)
		r.Get("/users/{id}/password", h.UserPasswordForm)
		r.Post("/users", h.UserCreate)
		r.Post("/users/{id}/password", h.UserPasswordSubmit)
		r.Delete("/users/{id}", h.UserDelete)
	})

	return r
}

// ChannelAction dispatches POST /channels/{id} to update or delete
// based on the hidden _method field. Used by HTML forms which only
// support GET/POST.
func (h *Handler) ChannelAction(w http.ResponseWriter, r *http.Request) {
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
		h.updateChannelByID(w, r, id)
	case "DELETE":
		h.deleteChannelByID(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// LoginPage renders the login form.
func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	// Already logged in? redirect
	if cookie, err := r.Cookie("llmrx_session"); err == nil {
		if u, _ := h.store.GetUserBySession(cookie.Value); u != nil {
			http.Redirect(w, r, "/admin/dashboard", http.StatusSeeOther)
			return
		}
	}
	if err := h.renderer.Render(w, "login_body", map[string]any{
		"Body":     "login_body",
		"Title":    "登录",
		"User":     nil,
		"Username": r.URL.Query().Get("username"),
		"Error":    r.URL.Query().Get("error"),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// LoginSubmit handles the form submit from LoginPage.
func (h *Handler) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.respondLoginError(w, r, "表单解析失败")
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	if username == "" || password == "" {
		h.respondLoginError(w, r, "请输入用户名和密码")
		return
	}
	u, err := h.store.GetUserByUsername(username)
	if err != nil || u == nil || u.Status != 1 {
		h.respondLoginError(w, r, "用户名或密码错误")
		return
	}
	if !auth.Verify(u.PasswordHash, password).OK {
		h.respondLoginError(w, r, "用户名或密码错误")
		return
	}

	tok := newSessionToken()
	u.SessionToken = tok
	exp := nowAdd(DefaultSessionTTL)
	u.SessionExp = &exp

	// Upgrade legacy password hashes to argon2id on successful login.
	if auth.IsLegacy(u.PasswordHash) || auth.IsBcrypt(u.PasswordHash) {
		if nh, err := auth.Hash(password); err == nil {
			u.PasswordHash = nh
		}
	}
	if err := h.store.UpdateUser(u); err != nil {
		h.respondLoginError(w, r, "无法持久化会话")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "llmrx_session",
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Expires:  exp,
		MaxAge:   int(DefaultSessionTTL.Seconds()),
		SameSite: http.SameSiteStrictMode,
	})

	// HTMX expects 200 + OOB swap to redirect (we use JS in template)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<div id="login-error"></div>`))
}

// LogoutSubmit clears the session and redirects to login.
func (h *Handler) LogoutSubmit(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("llmrx_session"); err == nil {
		if u, err := h.store.GetUserBySession(cookie.Value); err == nil && u != nil {
			u.SessionToken = ""
			u.SessionExp = nil
			_ = h.store.UpdateUser(u)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "llmrx_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// DashboardPage renders the dashboard.
func (h *Handler) DashboardPage(w http.ResponseWriter, r *http.Request) {
	user := getUser(r)
	tokens, _ := h.store.GetTokens()
	channels, _ := h.store.GetChannels()
	stats, _ := h.store.LogStats()

	activeTokens := 0
	for _, t := range tokens {
		if t.Status == 0 {
			activeTokens++
		}
	}
	activeChannels := 0
	for _, c := range channels {
		if int(c.Status) == 1 {
			activeChannels++
		}
	}

	data := map[string]any{
		"Body":           "dashboard_body",
		"Title":          "仪表盘",
		"User":           userToView(user),
		"Active":         "dashboard",
		"ActiveTokens":   activeTokens,
		"ActiveChannels": activeChannels,
		"TotalRequests":  stats.Total,
		"TotalCost":      stats.RealCostUSD,
	}
	if err := h.renderer.Render(w, "dashboard_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) respondLoginError(w http.ResponseWriter, r *http.Request, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	html := `<div id="login-error"><div class="p-3 rounded bg-red-100 text-red-800 border border-red-200 text-sm">` +
		escapeHTML(msg) + `</div></div>`
	_, _ = w.Write([]byte(html))
}

func userToView(u *model.User) *User {
	if u == nil {
		return nil
	}
	return &User{ID: u.ID, Username: u.Username, Role: int(u.Role)}
}
