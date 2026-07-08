package admin

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// ListPlans returns every plan, ordered by id. Used by the
// admin Plans page to render the table and by the Token form's
// plan selector.
func (h *Handler) ListPlans(w http.ResponseWriter, r *http.Request) {
	ps, err := h.store.GetPlans()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(ps)})
}

// GetPlan returns one plan by id. 404 when missing.
func (h *Handler) GetPlan(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	p, err := h.store.GetPlan(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "plan not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// CreatePlan inserts a new plan row. The id, used_usd, created_at
// and updated_at fields are server-assigned; the rest come from
// the request body.
func (h *Handler) CreatePlan(w http.ResponseWriter, r *http.Request) {
	var p model.Plan
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if p.Name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	if p.Status == 0 {
		p.Status = 1
	}
	if p.MarkupRatio <= 0 {
		p.MarkupRatio = 1.0
	}
	now := time.Now()
	p.CreatedAt = now
	p.UpdatedAt = now
	if err := h.store.CreatePlan(&p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// UpdatePlan patches a plan row. Only fields present in the body
// are touched; used_usd is not patchable (callers must use the
// spend increment path; UI shouldn't expose this).
func (h *Handler) UpdatePlan(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	cur, err := h.store.GetPlan(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "plan not found")
		return
	}
	var patch model.Plan
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if patch.Name != "" {
		cur.Name = patch.Name
	}
	if patch.BudgetUSD != 0 {
		cur.BudgetUSD = patch.BudgetUSD
	}
	if patch.MarkupRatio > 0 {
		cur.MarkupRatio = patch.MarkupRatio
	}
	if patch.Status != 0 {
		cur.Status = patch.Status
	}
	cur.UpdatedAt = time.Now()
	if err := h.store.UpdatePlan(cur); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cur)
}

// DeletePlan removes a plan. Before removing, the handler unlinks
// every token that still references the plan (sets plan_id=0) so
// that the chat pipeline doesn't try to read spend from a missing
// row. The handler is the right place to enforce this invariant
// since SQL-level CASCADE can't be added without migrating every
// existing deployment.
func (h *Handler) DeletePlan(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	tokens, err := h.store.GetTokens()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range tokens {
		if tokens[i].PlanID == id {
			tokens[i].PlanID = 0
			if err := h.store.UpdateToken(&tokens[i]); err != nil {
				writeErr(w, http.StatusInternalServerError,
					"unlink token "+itoa(tokens[i].ID)+": "+err.Error())
				return
			}
		}
	}
	if err := h.store.DeletePlan(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.tokens != nil {
		_ = h.tokens.Reload()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// itoa is a tiny int64→string helper to keep this file free of
// strconv noise (one usage above).
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
