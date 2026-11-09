package webui

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/secrets"
)

// ChannelsPage renders the channels list.
func (h *Handler) ChannelsPage(w http.ResponseWriter, r *http.Request) {
	h.channelsListPage(w, r, "")
}

// ChannelsListPartial returns just the table body for HTMX search.
func (h *Handler) ChannelsListPartial(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	h.channelsListPage(w, r, q)
}

func (h *Handler) channelsListPage(w http.ResponseWriter, r *http.Request, query string) {
	chs, err := h.store.GetChannels()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if query != "" {
		filtered := chs[:0]
		for _, c := range chs {
			if strings.Contains(strings.ToLower(c.Name), strings.ToLower(query)) ||
				strings.Contains(strings.ToLower(c.Provider), strings.ToLower(query)) {
				filtered = append(filtered, c)
			}
		}
		chs = filtered
	}

	// chi.Mount strips the prefix, so /admin/channels/partial/list
	// hits /partial/list here. We only want to render the body for
	// HTMX partial requests.
	if strings.HasPrefix(r.URL.Path, "/partial/") {
		if err := h.renderer.RenderPartial(w, "channels_table_body", map[string]any{
			"Channels": chs,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	data := map[string]any{
		"Body":     "channels_list_body",
		"Title":    "通道管理",
		"User":     userToView(getUser(r)),
		"Active":   "channels",
		"Channels": chs,
	}
	if err := h.renderer.Render(w, "channels_list_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ChannelNewForm renders the new channel form.
func (h *Handler) ChannelNewForm(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Body":      "channels_form_body",
		"Title":     "新建通道",
		"User":      userToView(getUser(r)),
		"Active":    "channels",
		"Providers": provider.AllProviders(),
	}
	if err := h.renderer.Render(w, "channels_form_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ChannelEditForm renders the edit form.
func (h *Handler) ChannelEditForm(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	ch, err := h.store.GetChannel(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	data := map[string]any{
		"Body":      "channels_form_body",
		"Title":     "编辑通道",
		"User":      userToView(getUser(r)),
		"Active":    "channels",
		"Channel":   ch,
		"Providers": provider.AllProviders(),
	}
	if err := h.renderer.Render(w, "channels_form_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// channelFormFields is the canonical ordered list of field names
// echoed back on validation errors. Referenced from multiple
// handlers so they all preserve the same set.
var channelFormFields = []string{
	"name", "provider", "base_url", "models", "intents",
	"priority", "input_price", "output_price", "status",
	"balance_threshold", "balance_url",
	"plan_kind", "plan_quota_metric", "plan_quota_limit",
	"plan_quota_window", "plan_quota_window_hours",
	"usage_probe_kind", "usage_probe_cookie", "usage_probe_endpoint",
	"usage_probe_json_path", "usage_probe_window", "usage_probe_unit",
	"usage_probe_interval",
}

var channelFormRenames = map[string]string{
	"models":            "ModelsStr",
	"intents":           "IntentsStr",
	"priority":          "PriorityStr",
	"input_price":       "InputPriceStr",
	"output_price":      "OutputPriceStr",
	"base_url":          "BaseURL",
	"balance_threshold": "BalanceThresholdStr",
}

// parseUsageProbe builds a model.UsageProbe from the channel form.
// An empty kind means "no probe" and clears the configuration. The
// cookie field is write-only: an empty value keeps the stored one.
func parseUsageProbe(r *http.Request, cur model.UsageProbe) (model.UsageProbe, error) {
	kind := strings.TrimSpace(r.FormValue("usage_probe_kind"))
	if kind == "" {
		return model.UsageProbe{}, nil
	}
	up := model.UsageProbe{
		Kind:        kind,
		EndpointURL: strings.TrimSpace(r.FormValue("usage_probe_endpoint")),
		JSONPath:    strings.TrimSpace(r.FormValue("usage_probe_json_path")),
		WindowName:  strings.TrimSpace(r.FormValue("usage_probe_window")),
		Unit:        strings.TrimSpace(r.FormValue("usage_probe_unit")),
		IntervalSec: int(parseIntDefault(r.FormValue("usage_probe_interval"), 0)),
		// Keep the existing sealed cookie unless the operator typed a
		// new one (it is never echoed back into the form).
		CookieCipher: cur.CookieCipher,
		CookieMasked: cur.CookieMasked,
	}
	if raw := strings.TrimSpace(r.FormValue("usage_probe_cookie")); raw != "" {
		sealed, err := sealCookie(raw)
		if err != nil {
			return up, err
		}
		up.CookieCipher = sealed
		up.CookieMasked = secrets.Mask(raw)
	}
	return up, up.Validate()
}

// sealFunc is installed by the server when the secrets manager is
// available; nil falls back to rejecting cookie probes with a clear
// message rather than storing a plaintext cookie.
var sealFunc func(string) (string, error)

// SetCookieSealer wires the AES-GCM sealer used for probe cookies.
func SetCookieSealer(fn func(string) (string, error)) { sealFunc = fn }

func sealCookie(raw string) (string, error) {
	if sealFunc == nil {
		return "", errors.New("usage probe cookie requires LLMRX_KEY_MASTER (no sealer configured)")
	}
	return sealFunc(raw)
}

// parsePlanQuota builds a model.PlanQuota from the channel form. An
// empty limit field means "not tracked" and clears whatever was set.
func parsePlanQuota(r *http.Request) (model.PlanQuota, error) {
	limitStr := strings.TrimSpace(r.FormValue("plan_quota_limit"))
	if limitStr == "" {
		return model.PlanQuota{}, nil
	}
	limit, err := strconv.ParseFloat(limitStr, 64)
	if err != nil {
		return model.PlanQuota{}, errors.New("plan_quota_limit 必须是数字")
	}
	pq := model.PlanQuota{
		Kind:        strings.TrimSpace(r.FormValue("plan_kind")),
		Metric:      strings.TrimSpace(r.FormValue("plan_quota_metric")),
		Limit:       limit,
		Window:      strings.TrimSpace(r.FormValue("plan_quota_window")),
		WindowHours: int(parseIntDefault(r.FormValue("plan_quota_window_hours"), 0)),
	}
	return pq, pq.Validate()
}

// ChannelCreate handles form POST to create a new channel.
func (h *Handler) ChannelCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderFormError(w, r, formErrorView{
			Body: "channels_form_body", Title: "新建通道",
			Active: "channels", Msg: "表单解析失败",
			Fields: channelFormFields, FieldRenames: channelFormRenames,
			Extras: map[string]any{"Providers": provider.AllProviders()},
		})
		return
	}

	providerVal := r.FormValue("provider")
	ch := &model.Channel{
		Name:        strings.TrimSpace(r.FormValue("name")),
		Provider:    providerVal,
		Protocol:    "openai", // default
		BaseURL:     strings.TrimSpace(r.FormValue("base_url")),
		Models:      splitLines(r.FormValue("models")),
		Intents:     splitLines(r.FormValue("intents")),
		Priority:    parseIntDefault(r.FormValue("priority"), 5),
		InputPrice:  parseFloatDefault(r.FormValue("input_price"), 0),
		OutputPrice: parseFloatDefault(r.FormValue("output_price"), 0),
		// P14: low-balance alert line (in the channel's currency) and
		// an optional balance endpoint override for relays.
		BalanceThreshold: parseFloatDefault(r.FormValue("balance_threshold"), 0),
		BalanceURL:       strings.TrimSpace(r.FormValue("balance_url")),
		Status:           model.ChannelEnabled,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	if r.FormValue("status") != "1" {
		ch.Status = model.ChannelDisabled
	}
	pq, err := parsePlanQuota(r)
	if err != nil {
		h.renderFormError(w, r, formErrorView{
			Body: "channels_form_body", Title: "新建通道",
			Active: "channels", Msg: err.Error(), Form: r.Form,
			Fields: channelFormFields, FieldRenames: channelFormRenames,
			Extras: map[string]any{"Providers": provider.AllProviders()},
		})
		return
	}
	ch.PlanQuota = pq
	up, uerr := parseUsageProbe(r, model.UsageProbe{})
	if uerr != nil {
		h.renderFormError(w, r, formErrorView{
			Body: "channels_form_body", Title: "新建通道",
			Active: "channels", Msg: uerr.Error(), Form: r.Form,
			Fields: channelFormFields, FieldRenames: channelFormRenames,
			Extras: map[string]any{"Providers": provider.AllProviders()},
		})
		return
	}
	ch.UsageProbe = up

	if err := validateChannel(ch); err != nil {
		h.renderFormError(w, r, formErrorView{
			Body: "channels_form_body", Title: "新建通道",
			Active: "channels", Msg: err.Error(), Form: r.Form,
			Fields: channelFormFields, FieldRenames: channelFormRenames,
			Extras: map[string]any{"Providers": provider.AllProviders()},
		})
		return
	}
	if err := h.store.CreateChannel(ch); err != nil {
		h.renderFormError(w, r, formErrorView{
			Body: "channels_form_body", Title: "新建通道",
			Active: "channels", Record: ch,
			Msg: "创建失败: " + err.Error(), Form: r.Form,
			Fields: channelFormFields, FieldRenames: channelFormRenames,
			Extras: map[string]any{"Providers": provider.AllProviders()},
		})
		return
	}
	h.triggerReload()
	http.Redirect(w, r, "/admin/channels", http.StatusSeeOther)
}

// updateChannelByID implements form PUT + hx-delete for a channel.
func (h *Handler) updateChannelByID(w http.ResponseWriter, r *http.Request, id int64) {
	cur, err := h.store.GetChannel(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderFormError(w, r, formErrorView{
			Body: "channels_form_body", Title: "编辑通道",
			Active: "channels", Record: cur, Msg: "表单解析失败",
			Fields: channelFormFields, FieldRenames: channelFormRenames,
			Extras: map[string]any{"Providers": provider.AllProviders()},
		})
		return
	}

	if v := strings.TrimSpace(r.FormValue("name")); v != "" {
		cur.Name = v
	}
	if v := r.FormValue("provider"); v != "" {
		cur.Provider = v
	}
	if v := strings.TrimSpace(r.FormValue("base_url")); v != "" {
		cur.BaseURL = v
	}
	if v := r.FormValue("models"); v != "" {
		cur.Models = splitLines(v)
	}
	cur.Intents = splitLines(r.FormValue("intents"))
	if v := r.FormValue("priority"); v != "" {
		cur.Priority = parseIntDefault(v, cur.Priority)
	}
	if v := r.FormValue("input_price"); v != "" {
		cur.InputPrice = parseFloatDefault(v, cur.InputPrice)
	}
	if v := r.FormValue("output_price"); v != "" {
		cur.OutputPrice = parseFloatDefault(v, cur.OutputPrice)
	}
	// Both balance fields are settable to zero/empty (clearing the
	// threshold disables monitoring for this channel), so unlike the
	// numeric fields above they are applied unconditionally.
	if v := strings.TrimSpace(r.FormValue("balance_threshold")); v != "" {
		cur.BalanceThreshold = parseFloatDefault(v, cur.BalanceThreshold)
	}
	cur.BalanceURL = strings.TrimSpace(r.FormValue("balance_url"))
	// Plan quota replaces unconditionally: an empty limit field means
	// "stop tracking", matching the balance-clearing semantics.
	pq, err := parsePlanQuota(r)
	if err != nil {
		h.renderFormError(w, r, formErrorView{
			Body: "channels_form_body", Title: "编辑通道",
			Active: "channels", Record: cur, Msg: err.Error(), Form: r.Form,
			Fields: channelFormFields, FieldRenames: channelFormRenames,
			Extras: map[string]any{"Providers": provider.AllProviders()},
		})
		return
	}
	cur.PlanQuota = pq
	up, uerr := parseUsageProbe(r, cur.UsageProbe)
	if uerr != nil {
		h.renderFormError(w, r, formErrorView{
			Body: "channels_form_body", Title: "编辑通道",
			Active: "channels", Record: cur, Msg: uerr.Error(), Form: r.Form,
			Fields: channelFormFields, FieldRenames: channelFormRenames,
			Extras: map[string]any{"Providers": provider.AllProviders()},
		})
		return
	}
	cur.UsageProbe = up
	cur.Status = model.ChannelDisabled
	if r.FormValue("status") == "1" {
		cur.Status = model.ChannelEnabled
	}
	cur.UpdatedAt = time.Now()

	if err := validateChannel(cur); err != nil {
		h.renderFormError(w, r, formErrorView{
			Body: "channels_form_body", Title: "编辑通道",
			Active: "channels", Record: cur, Msg: err.Error(), Form: r.Form,
			Fields: channelFormFields, FieldRenames: channelFormRenames,
			Extras: map[string]any{"Providers": provider.AllProviders()},
		})
		return
	}
	if err := h.store.UpdateChannel(cur); err != nil {
		h.renderFormError(w, r, formErrorView{
			Body: "channels_form_body", Title: "编辑通道",
			Active: "channels", Record: cur, Msg: "更新失败: " + err.Error(), Form: r.Form,
			Fields: channelFormFields, FieldRenames: channelFormRenames,
			Extras: map[string]any{"Providers": provider.AllProviders()},
		})
		return
	}
	h.triggerReload()
	http.Redirect(w, r, "/admin/channels", http.StatusSeeOther)
}

// ChannelDelete handles DELETE for a channel (HTMX).
func (h *Handler) ChannelDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	h.deleteChannelByID(w, r, id)
}

// deleteChannelByID is the inner implementation of ChannelDelete.
func (h *Handler) deleteChannelByID(w http.ResponseWriter, r *http.Request, id int64) {
	if err := h.store.DeleteChannel(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.triggerReload()
	w.WriteHeader(http.StatusOK)
}

// ChannelKeysPage renders the keys management page.
func (h *Handler) ChannelKeysPage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	ch, err := h.store.GetChannel(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	keys, err := h.store.GetKeys(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Body":    "channels_keys_body",
		"Title":   ch.Name + " - Keys",
		"User":    userToView(getUser(r)),
		"Active":  "channels",
		"Channel": ch,
		"Keys":    keys,
	}
	if err := h.renderer.Render(w, "channels_keys_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ChannelKeyCreate handles form POST to add a key.
func (h *Handler) ChannelKeyCreate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	plain := strings.TrimSpace(r.FormValue("key"))
	if plain == "" {
		http.Error(w, "key required", http.StatusBadRequest)
		return
	}
	k := &model.Key{
		ChannelID: id,
		Key:       plain,
		KeyMasked: secrets.Mask(plain),
		Status:    model.KeyActive,
		CreatedAt: time.Now(),
	}
	if err := h.store.CreateKey(k); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.triggerReload()
	// HTMX: render the new key row
	if err := h.renderer.RenderPartial(w, "key_row", map[string]any{
		"ID":        k.ID,
		"KeyMasked": k.KeyMasked,
		"CreatedAt": k.CreatedAt,
		"ChannelID": id,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ChannelKeyDelete handles DELETE for a key.
func (h *Handler) ChannelKeyDelete(w http.ResponseWriter, r *http.Request) {
	keyID, err := strconv.ParseInt(chi.URLParam(r, "keyId"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := h.store.DeleteKey(keyID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.triggerReload()
	w.WriteHeader(http.StatusOK)
}

// triggerReload asks the admin handler to rebuild in-memory state.
// This is best-effort: if the admin bridge is nil or the call fails,
// the page still works (the next periodic reload will catch up).
func (h *Handler) triggerReload() {
	if h.adminH == nil {
		return
	}
	if err := h.adminH.TriggerReload(); err != nil {
		// Log to stderr; non-fatal
		// (real logging would be wired through a logger)
		_ = err
	}
}

// ---------- helpers ----------

func splitLines(s string) []string {
	if s == "" {
		return []string{}
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

func parseFloatDefault(s string, def float64) float64 {
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return v
}

func firstOrEmpty(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

func validateChannel(ch *model.Channel) error {
	if strings.TrimSpace(ch.Name) == "" {
		return errBad("名称必填")
	}
	if strings.TrimSpace(ch.Provider) == "" {
		return errBad("Provider 必填")
	}
	if strings.TrimSpace(ch.BaseURL) == "" {
		return errBad("Base URL 必填")
	}
	if len(ch.Models) == 0 {
		return errBad("至少需要一个模型")
	}
	if ch.InputPrice < 0 || ch.OutputPrice < 0 {
		return errBad("价格不能为负")
	}
	return nil
}

type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }
func errBad(s string) error              { return &validationError{msg: s} }
