package webui

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/model"
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
		"Body":   "channels_form_body",
		"Title":  "新建通道",
		"User":   userToView(getUser(r)),
		"Active": "channels",
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
		"Body":    "channels_form_body",
		"Title":   "编辑通道",
		"User":    userToView(getUser(r)),
		"Active":  "channels",
		"Channel": ch,
	}
	if err := h.renderer.Render(w, "channels_form_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ChannelCreate handles form POST to create a new channel.
func (h *Handler) ChannelCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderFormError(w, r, nil, "表单解析失败", nil)
		return
	}

	provider := r.FormValue("provider")
	ch := &model.Channel{
		Name:      strings.TrimSpace(r.FormValue("name")),
		Provider:  provider,
		Protocol:  "openai", // default
		BaseURL:   strings.TrimSpace(r.FormValue("base_url")),
		Models:    splitLines(r.FormValue("models")),
		Intents:   splitLines(r.FormValue("intents")),
		Priority:  parseIntDefault(r.FormValue("priority"), 5),
		InputPrice: parseFloatDefault(r.FormValue("input_price"), 0),
		OutputPrice: parseFloatDefault(r.FormValue("output_price"), 0),
		Status:    model.ChannelEnabled,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if r.FormValue("status") != "1" {
		ch.Status = model.ChannelDisabled
	}

	if err := validateChannel(ch); err != nil {
		h.renderFormError(w, r, nil, err.Error(), r.Form)
		return
	}
	if err := h.store.CreateChannel(ch); err != nil {
		h.renderFormError(w, r, ch, "创建失败: "+err.Error(), r.Form)
		return
	}
	h.triggerReload()
	http.Redirect(w, r, "/admin/channels", http.StatusSeeOther)
}

// ChannelUpdate handles form PUT to update a channel.
func (h *Handler) ChannelUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	h.updateChannelByID(w, r, id)
}

// updateChannelByID is the inner implementation of ChannelUpdate,
// shared with the POST+_method form handler.
func (h *Handler) updateChannelByID(w http.ResponseWriter, r *http.Request, id int64) {
	cur, err := h.store.GetChannel(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderFormError(w, r, cur, "表单解析失败", nil)
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
	cur.Status = model.ChannelDisabled
	if r.FormValue("status") == "1" {
		cur.Status = model.ChannelEnabled
	}
	cur.UpdatedAt = time.Now()

	if err := validateChannel(cur); err != nil {
		h.renderFormError(w, r, cur, err.Error(), r.Form)
		return
	}
	if err := h.store.UpdateChannel(cur); err != nil {
		h.renderFormError(w, r, cur, "更新失败: "+err.Error(), r.Form)
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
		"ID":         k.ID,
		"KeyMasked":  k.KeyMasked,
		"CreatedAt":  k.CreatedAt,
		"ChannelID":  id,
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

func (h *Handler) renderFormError(w http.ResponseWriter, r *http.Request, ch *model.Channel, msg string, form map[string][]string) {
	fd := map[string]string{}
	if form != nil {
		fd["Name"] = firstOrEmpty(form["name"])
		fd["Provider"] = firstOrEmpty(form["provider"])
		fd["BaseURL"] = firstOrEmpty(form["base_url"])
		fd["ModelsStr"] = firstOrEmpty(form["models"])
		fd["IntentsStr"] = firstOrEmpty(form["intents"])
		fd["PriorityStr"] = firstOrEmpty(form["priority"])
		fd["InputPriceStr"] = firstOrEmpty(form["input_price"])
		fd["OutputPriceStr"] = firstOrEmpty(form["output_price"])
		fd["Status"] = firstOrEmpty(form["status"])
	}
	data := map[string]any{
		"Body":      "channels_form_body",
		"Title":     "通道表单",
		"User":      userToView(getUser(r)),
		"Active":    "channels",
		"Channel":   ch,
		"FormError": msg,
		"FormData":  fd,
	}
	if err := h.renderer.Render(w, "channels_form_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
