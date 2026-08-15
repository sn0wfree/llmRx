package webui

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/model"
)

// usageRange resolves the ?range= filter to a from-timestamp.
func usageRange(now time.Time, rangeVal string) int64 {
	switch rangeVal {
	case "24h":
		return now.Add(-24 * time.Hour).Unix()
	case "7d":
		return now.Add(-7 * 24 * time.Hour).Unix()
	case "30d":
		return now.Add(-30 * 24 * time.Hour).Unix()
	case "90d":
		return now.Add(-90 * 24 * time.Hour).Unix()
	}
	return 0
}

// userTokenIDs resolves the token ids owned by a user for log queries.
func (h *Handler) userTokenIDs(userID int64) []int64 {
	toks, err := h.store.GetTokensByUserID(userID)
	if err != nil || len(toks) == 0 {
		// Fall back to the registration contract (token.name == username).
		u, uerr := h.store.GetUser(userID)
		if uerr != nil || u == nil {
			return nil
		}
		if tok, terr := h.store.GetTokenByName(u.Username); terr == nil && tok != nil {
			return []int64{tok.ID}
		}
		return nil
	}
	ids := make([]int64, 0, len(toks))
	for _, t := range toks {
		ids = append(ids, t.ID)
	}
	return ids
}

// usageSummary aggregates a user's logs for a time range.
type usageSummary struct {
	Requests         int64
	Errors           int64
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	CacheHits        int64
	RealCostUSD      float64
	BilledCostUSD    float64
}

// summarize computes a user's usage over [from, now].
func (h *Handler) summarize(tokenIDs []int64, from int64) usageSummary {
	var s usageSummary
	if len(tokenIDs) == 0 {
		return s
	}
	filter := logstore.QueryFilter{TokenIDs: tokenIDs, Limit: 50}
	if from > 0 {
		filter.CreatedFrom = from
	}
	series, _ := h.logStore.TimeSeries(filter, 0, nil)
	for _, b := range series {
		s.Requests += b.Requests
		s.Errors += b.Errors
		s.PromptTokens += b.PromptTokens
		s.CompletionTokens += b.CompletionTokens
		s.RealCostUSD += b.RealCostUSD
		s.BilledCostUSD += b.BilledCostUSD
	}
	// Cache-hit rows are counted separately (they reuse the stored
	// usage, so their token totals already appear in the series).
	chFilter := filter
	chFilter.CacheHit = true
	s.CacheHits = h.countCacheHits(chFilter)
	return s
}

func (h *Handler) countCacheHits(filter logstore.QueryFilter) int64 {
	_, total, err := h.logStore.Query(filter, nil)
	if err != nil {
		return 0
	}
	return total
}

// userUsageView is the per-user aggregate used by the admin page.
type userUsageView struct {
	User     *model.User
	TokenIDs []int64
	Summary  usageSummary
}

// UsersUsagePage lists all users with their aggregate usage.
func (h *Handler) UsersUsagePage(w http.ResponseWriter, r *http.Request) {
	rangeVal := r.URL.Query().Get("range")
	now := time.Now().UTC()
	from := usageRange(now, rangeVal)

	users, err := h.store.GetUsers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	views := make([]userUsageView, 0, len(users))
	for i := range users {
		u := &users[i]
		ids := h.userTokenIDs(u.ID)
		views = append(views, userUsageView{User: u, TokenIDs: ids, Summary: h.summarize(ids, from)})
	}

	data := map[string]any{
		"Body":   "users_usage_body",
		"Title":  "用量 · 用户",
		"User":   userToView(getUser(r)),
		"Active": "usage",
		"Views":  views,
		"Range":  rangeVal,
	}
	if err := h.renderer.Render(w, "users_usage_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// UserUsagePage renders one user's usage: totals, daily trend, by
// model, and the request list.
func (h *Handler) UserUsagePage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad user id", http.StatusBadRequest)
		return
	}
	u, err := h.store.GetUser(id)
	if err != nil || u == nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	rangeVal := r.URL.Query().Get("range")
	modelFilter := r.URL.Query().Get("model")
	now := time.Now().UTC()
	from := usageRange(now, rangeVal)
	ids := h.userTokenIDs(id)

	summary := h.summarize(ids, from)

	series, _ := h.logStore.TimeSeries(logstore.QueryFilter{TokenIDs: ids, Limit: 500, CreatedFrom: from}, 86400, nil)
	byModel, _ := h.logStore.TopByField(logstore.QueryFilter{TokenIDs: ids, Limit: 500, CreatedFrom: from}, "model", 20, nil)

	page := 0
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}
	limit := 50
	logFilter := logstore.QueryFilter{TokenIDs: ids, CreatedFrom: from, Model: modelFilter, Limit: limit, Offset: page * limit}
	logs, _, err := h.logStore.Query(logFilter, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	seriesJSON := "[]"
	if len(series) > 0 {
		if b, err := json.Marshal(series); err == nil {
			seriesJSON = string(b)
		}
	}

	data := map[string]any{
		"Body":       "user_usage_body",
		"Title":      "用量 · " + u.Username,
		"User":       userToView(getUser(r)),
		"Active":     "usage",
		"TargetUser": u,
		"Summary":    summary,
		"ByModel":    byModel,
		"SeriesJSON": seriesJSON,
		"Logs":       logs,
		"Range":      rangeVal,
		"Model":      modelFilter,
		"Page":       page,
	}
	if err := h.renderer.Render(w, "user_usage_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// UserUsageExportCSV streams a user's request log as CSV.
func (h *Handler) UserUsageExportCSV(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad user id", http.StatusBadRequest)
		return
	}
	u, err := h.store.GetUser(id)
	if err != nil || u == nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	rangeVal := r.URL.Query().Get("range")
	from := usageRange(time.Now().UTC(), rangeVal)
	ids := h.userTokenIDs(id)
	if len(ids) == 0 {
		http.Error(w, "user has no tokens", http.StatusBadRequest)
		return
	}

	logs, _, err := h.logStore.Query(logstore.QueryFilter{TokenIDs: ids, CreatedFrom: from, Limit: 5000}, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-usage.csv"`, u.Username))
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"time", "model", "prompt_tokens", "completion_tokens", "cached_tokens", "reasoning_tokens", "real_cost_usd", "billed_cost_usd", "status", "cache_hit", "router_path"})
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
			l.RouterPath,
		})
	}
}
