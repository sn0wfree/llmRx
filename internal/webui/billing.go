package webui

import (
	"errors"
	"net/http"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

// RegisterPage renders the public invite-code registration form.
func (h *Handler) RegisterPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Body":  "register_body",
		"Title": "注册 · llmRx",
	}
	if err := h.renderer.Render(w, "register_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// MePage renders the signed-in user's account page: API key, plan
// budget and the top-up redemption form.
func (h *Handler) MePage(w http.ResponseWriter, r *http.Request) {
	user := getUser(r)
	me, err := h.userBillingView(user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Body":  "me_body",
		"Title": "我的账户 · llmRx",
		"User":  userToView(user),
		"Me":    me,
		// BaseURL is the gateway's own origin for client config.
		"BaseURL": "http://" + r.Host,
	}
	if err := h.renderer.Render(w, "me_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// RedeemCodesPage renders the admin code-management page.
func (h *Handler) RedeemCodesPage(w http.ResponseWriter, r *http.Request) {
	codes, err := h.store.ListRedeemCodes()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Body":   "redeem_codes_body",
		"Title":  "邀请码 / 充值码",
		"User":   userToView(getUser(r)),
		"Active": "redeem_codes",
		"Codes":  codes,
	}
	if err := h.renderer.Render(w, "redeem_codes_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// meView is the account page data.
type meView struct {
	Username  string      `json:"username"`
	APIKey    string      `json:"api_key"`
	PlanID    int64       `json:"plan_id"`
	PlanName  string      `json:"plan_name"`
	BudgetUSD float64     `json:"budget_usd"`
	UsedUSD   float64     `json:"used_usd"`
	Role      model.UserRole `json:"role"`
}

// userBillingView resolves the user's dedicated token + plan (the
// token created at registration carries the plan).
func (h *Handler) userBillingView(u *model.User) (*meView, error) {
	tok, err := h.store.GetTokenByName(u.Username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &meView{Username: u.Username, Role: u.Role}, nil
		}
		return nil, err
	}
	view := &meView{Username: u.Username, Role: u.Role}
	if tok.PlanID <= 0 {
		return view, nil
	}
	view.APIKey = tok.Key
	view.PlanID = tok.PlanID
	plan, err := h.store.GetPlan(tok.PlanID)
	if err != nil {
		return nil, err
	}
	view.PlanName = plan.Name
	view.BudgetUSD = plan.BudgetUSD
	view.UsedUSD = plan.UsedUSD
	return view, nil
}
