package webui

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/model"
)

// PlansPage renders the plans list.
func (h *Handler) PlansPage(w http.ResponseWriter, r *http.Request) {
	plans, err := h.store.GetPlans()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Body":   "plans_list_body",
		"Title":  "计划管理",
		"User":   userToView(getUser(r)),
		"Active": "plans",
		"Plans":  plans,
	}
	if err := h.renderer.Render(w, "plans_list_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// PlanNewForm renders the new plan form.
func (h *Handler) PlanNewForm(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Body":   "plans_form_body",
		"Title":  "新建计划",
		"User":   userToView(getUser(r)),
		"Active": "plans",
	}
	if err := h.renderer.Render(w, "plans_form_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// PlanEditForm renders the edit form.
func (h *Handler) PlanEditForm(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	p, err := h.store.GetPlan(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	data := map[string]any{
		"Body":   "plans_form_body",
		"Title":  "编辑计划",
		"User":   userToView(getUser(r)),
		"Active": "plans",
		"Plan":   p,
	}
	if err := h.renderer.Render(w, "plans_form_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

var planFormFields = []string{"name", "budget_usd", "markup_ratio", "status"}

var planFormRenames = map[string]string{
	"budget_usd":   "BudgetStr",
	"markup_ratio": "MarkupStr",
}

// PlanCreate handles form POST to create a new plan.
func (h *Handler) PlanCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderFormError(w, r, formErrorView{
			Body: "plans_form_body", Title: "新建计划",
			Active: "plans", Msg: "表单解析失败",
			Fields: planFormFields, FieldRenames: planFormRenames,
		})
		return
	}
	p := &model.Plan{
		Name:        strings.TrimSpace(r.FormValue("name")),
		BudgetUSD:   parseFloatDefault(r.FormValue("budget_usd"), 0),
		MarkupRatio: parseFloatDefault(r.FormValue("markup_ratio"), 1.0),
		Status:      1,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if r.FormValue("status") != "1" {
		p.Status = 0
	}
	if p.Name == "" {
		h.renderFormError(w, r, formErrorView{
			Body: "plans_form_body", Title: "新建计划",
			Active: "plans", Msg: "名称必填", Form: r.Form,
			Fields: planFormFields, FieldRenames: planFormRenames,
		})
		return
	}
	if p.MarkupRatio <= 0 {
		p.MarkupRatio = 1.0
	}
	if err := h.store.CreatePlan(p); err != nil {
		h.renderFormError(w, r, formErrorView{
			Body: "plans_form_body", Title: "新建计划",
			Active: "plans", Record: p, Msg: "创建失败: " + err.Error(), Form: r.Form,
			Fields: planFormFields, FieldRenames: planFormRenames,
		})
		return
	}
	h.triggerReload()
	http.Redirect(w, r, "/admin/plans", http.StatusSeeOther)
}

// PlanAction dispatches POST /plans/{id} to update/delete based on _method.
func (h *Handler) PlanAction(w http.ResponseWriter, r *http.Request) {
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
		h.updatePlanByID(w, r, id)
	case "DELETE":
		h.deletePlanByID(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) updatePlanByID(w http.ResponseWriter, r *http.Request, id int64) {
	cur, err := h.store.GetPlan(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderFormError(w, r, formErrorView{
			Body: "plans_form_body", Title: "编辑计划",
			Active: "plans", Record: cur, Msg: "表单解析失败",
			Fields: planFormFields, FieldRenames: planFormRenames,
		})
		return
	}
	if v := strings.TrimSpace(r.FormValue("name")); v != "" {
		cur.Name = v
	}
	if v := r.FormValue("budget_usd"); v != "" {
		cur.BudgetUSD = parseFloatDefault(v, cur.BudgetUSD)
	}
	if v := r.FormValue("markup_ratio"); v != "" {
		if m := parseFloatDefault(v, 0); m > 0 {
			cur.MarkupRatio = m
		}
	}
	cur.Status = 0
	if r.FormValue("status") == "1" {
		cur.Status = 1
	}
	cur.UpdatedAt = time.Now()
	if err := h.store.UpdatePlan(cur); err != nil {
		h.renderFormError(w, r, formErrorView{
			Body: "plans_form_body", Title: "编辑计划",
			Active: "plans", Record: cur,
			Msg: "更新失败: " + err.Error(), Form: r.Form,
			Fields: planFormFields, FieldRenames: planFormRenames,
		})
		return
	}
	h.triggerReload()
	http.Redirect(w, r, "/admin/plans", http.StatusSeeOther)
}

func (h *Handler) deletePlanByID(w http.ResponseWriter, r *http.Request, id int64) {
	// Unlink tokens that reference this plan (mirrors the JSON API).
	tokens, _ := h.store.GetTokens()
	for i := range tokens {
		if tokens[i].PlanID == id {
			tokens[i].PlanID = 0
			_ = h.store.UpdateToken(&tokens[i])
		}
	}
	if err := h.store.DeletePlan(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.triggerReload()
	w.WriteHeader(http.StatusOK)
}
