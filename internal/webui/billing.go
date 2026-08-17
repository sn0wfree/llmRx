package webui

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/sn0wfree/llmRx/internal/logstore"
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

// MePage renders the signed-in user's home page: API key and balance.
func (h *Handler) MePage(w http.ResponseWriter, r *http.Request) {
	user := getUser(r)
	me, err := h.userBillingView(user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Today's summary for the quick overview card.
	now := time.Now().UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Unix()
	ids := h.userTokenIDs(user.ID)
	todaySummary := h.summarize(ids, todayStart)

	// Recent activity: last 5 requests for the dashboard table.
	var recentLogs []model.Log
	if len(ids) > 0 {
		logs, _, qerr := h.logStore.Query(logstore.QueryFilter{
			TokenIDs: ids,
			Limit:    5,
		}, nil)
		if qerr == nil {
			recentLogs = logs
		}
	}

	data := map[string]any{
		"Body":         "me_body",
		"Title":        "首页 · llmRx",
		"User":         userToView(user),
		"Me":           me,
		"RecentLogs":   recentLogs,
		"TodaySummary": todaySummary,
		"BaseURL":      "http://" + r.Host,
	}
	if err := h.renderer.Render(w, "me_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// MeUsagePage renders the signed-in user's detailed usage statistics.
func (h *Handler) MeUsagePage(w http.ResponseWriter, r *http.Request) {
	user := getUser(r)
	me, err := h.userBillingView(user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	rangeVal := r.URL.Query().Get("range")
	now := time.Now().UTC()
	from := usageRange(now, rangeVal)
	ids := h.userTokenIDs(user.ID)
	summary := h.summarize(ids, from)
	series, _ := h.logStore.TimeSeries(logstore.QueryFilter{TokenIDs: ids, Limit: 500, CreatedFrom: from}, 86400, nil)
	byModel, _ := h.logStore.TopByField(logstore.QueryFilter{TokenIDs: ids, Limit: 500, CreatedFrom: from}, "model", 20, nil)

	seriesJSON := "[]"
	if len(series) > 0 {
		if b, err := json.Marshal(series); err == nil {
			seriesJSON = string(b)
		}
	}

	data := map[string]any{
		"Body":       "me_usage_body",
		"Title":      "用量统计 · llmRx",
		"User":       userToView(user),
		"Me":         me,
		"Range":      rangeVal,
		"Summary":    summary,
		"ByModel":    byModel,
		"SeriesJSON": seriesJSON,
	}
	if err := h.renderer.Render(w, "me_usage_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// MeTopupPage renders the redemption code page.
func (h *Handler) MeTopupPage(w http.ResponseWriter, r *http.Request) {
	user := getUser(r)
	me, err := h.userBillingView(user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Body":  "me_topup_body",
		"Title": "充值 · llmRx",
		"User":  userToView(user),
		"Me":    me,
	}
	if err := h.renderer.Render(w, "me_topup_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// MeUsageExportCSV streams the signed-in user's request log as CSV.
func (h *Handler) MeUsageExportCSV(w http.ResponseWriter, r *http.Request) {
	user := getUser(r)
	rangeVal := r.URL.Query().Get("range")
	from := usageRange(time.Now().UTC(), rangeVal)
	ids := h.userTokenIDs(user.ID)
	if len(ids) == 0 {
		http.Error(w, "no usage", http.StatusBadRequest)
		return
	}
	logs, _, err := h.logStore.Query(logstore.QueryFilter{TokenIDs: ids, CreatedFrom: from, Limit: 5000}, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-usage.csv"`, user.Username))
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"time", "model", "prompt_tokens", "completion_tokens", "cached_tokens", "reasoning_tokens", "real_cost_usd", "billed_cost_usd", "status", "cache_hit"})
	for _, l := range logs {
		_ = cw.Write([]string{
			l.CreatedAt.UTC().Format(time.RFC3339),
			l.Model,
			strconv.Itoa(l.PromptTokens),
			strconv.Itoa(l.CompletionTokens),
			strconv.Itoa(l.CachedTokens),
			strconv.Itoa(l.ReasoningTokens),
			fmt.Sprintf("%.6f", l.RealCostUSD),
			fmt.Sprintf("%.6f", l.BilledCostUSD),
			strconv.Itoa(l.StatusCode),
			fmt.Sprintf("%v", l.CacheHit),
		})
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
	Username    string         `json:"username"`
	APIKey      string         `json:"api_key"`
	PlanID      int64          `json:"plan_id"`
	PlanName    string         `json:"plan_name"`
	BudgetUSD   float64        `json:"budget_usd"`
	UsedUSD     float64        `json:"used_usd"`
	RemainingUSD float64       `json:"remaining_usd"`
	Role        model.UserRole `json:"role"`
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
	view.RemainingUSD = plan.BudgetUSD - plan.UsedUSD
	return view, nil
}
