package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/sse"

	"github.com/sn0wfree/llmRx/internal/logstore"
)

// LogsPage renders the logs list with filter form.
func (h *Handler) LogsPage(w http.ResponseWriter, r *http.Request) {
	f := logstore.QueryFilter{Limit: 100, Offset: 0}
	if model := r.URL.Query().Get("model"); model != "" {
		f.Model = model
	}
	if tid := r.URL.Query().Get("token_id"); tid != "" {
		f.TokenID, _ = strconv.ParseInt(tid, 10, 64)
	}
	if cid := r.URL.Query().Get("channel_id"); cid != "" {
		f.ChannelID, _ = strconv.ParseInt(cid, 10, 64)
	}
	if sc := r.URL.Query().Get("status_code"); sc != "" {
		f.StatusCode, _ = strconv.Atoi(sc)
	}
	if from := r.URL.Query().Get("from"); from != "" {
		if t, err := time.Parse("2006-01-02", from); err == nil {
			f.CreatedFrom = t.Unix()
		}
	}
	if to := r.URL.Query().Get("to"); to != "" {
		if t, err := time.Parse("2006-01-02", to); err == nil {
			f.CreatedTo = t.Unix() + 86400
		}
	}
	logs, _, err := h.logStore.Query(f, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Body":       "logs_index_body",
		"Title":      "日志",
		"User":       userToView(getUser(r)),
		"Active":     "logs",
		"Logs":       logs,
		"Filter":     f,
		"FilterStr":  r.URL.RawQuery,
		"FilterFrom": r.URL.Query().Get("from"),
		"FilterTo":   r.URL.Query().Get("to"),
	}
	if err := h.renderer.Render(w, "logs_index_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// LogsStream serves the live SSE log stream: sse.Writer + broker
// subscribe + heartbeat, mirroring the legacy admin handler's
// StreamLogs. The page connects with EventSource and renders "log"
// events. Returns 503 when no broker is wired (no live logs).
func (h *Handler) LogsStream(w http.ResponseWriter, r *http.Request) {
	if h.adminH == nil || h.adminH.logBroker == nil {
		http.Error(w, "log streaming not configured", http.StatusServiceUnavailable)
		return
	}
	w2, err := sse.New(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := w2.Comment("hello llmRx logs"); err != nil {
		return
	}
	ch, unsub, err := h.adminH.logBroker.Subscribe()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer unsub()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go w2.Heartbeat(ctx, 15*time.Second)

	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-ch:
			if !ok {
				return
			}
			if err := w2.EventJSON("log", entry); err != nil {
				return
			}
		}
	}
}

// AlertsPage renders the alerts list + events.
func (h *Handler) AlertsPage(w http.ResponseWriter, r *http.Request) {
	alerts, err := h.store.GetAlerts()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	events, err := h.store.GetAlertEvents(50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Body":        "alerts_list_body",
		"Title":       "告警",
		"User":        userToView(getUser(r)),
		"Active":      "alerts",
		"Alerts":      alerts,
		"AlertEvents": events,
	}
	if err := h.renderer.Render(w, "alerts_list_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// AlertNewForm renders the new alert form.
func (h *Handler) AlertNewForm(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Body":   "alerts_form_body",
		"Title":  "新建告警",
		"User":   userToView(getUser(r)),
		"Active": "alerts",
	}
	if err := h.renderer.Render(w, "alerts_form_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// AlertEditForm renders the edit form.
func (h *Handler) AlertEditForm(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	a, err := h.store.GetAlert(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	data := map[string]any{
		"Body":   "alerts_form_body",
		"Title":  "编辑告警",
		"User":   userToView(getUser(r)),
		"Active": "alerts",
		"Alert":  a,
	}
	if err := h.renderer.Render(w, "alerts_form_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// AlertCreate handles POST to create an alert.
func (h *Handler) AlertCreate(w http.ResponseWriter, r *http.Request) {
	h.alertSave(w, r, nil)
}

// AlertAction dispatches POST /alerts/{id} to update/delete/ack.
func (h *Handler) AlertAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	// hx-delete sends a real DELETE with no form body.
	if r.Method == http.MethodDelete {
		h.alertDelete(w, r, id)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch strings.ToUpper(r.FormValue("_method")) {
	case "PUT":
		h.alertUpdate(w, r, id)
	case "DELETE":
		h.alertDelete(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) alertUpdate(w http.ResponseWriter, r *http.Request, id int64) {
	cur, err := h.store.GetAlert(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	h.alertSave(w, r, cur)
}

func (h *Handler) alertDelete(w http.ResponseWriter, r *http.Request, id int64) {
	if err := h.store.DeleteAlert(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) alertSave(w http.ResponseWriter, r *http.Request, existing interface{}) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a := &model.Alert{
		Name:        strings.TrimSpace(r.FormValue("name")),
		Type:        model.AlertType(r.FormValue("type")),
		Threshold:   parseFloatForm(r.FormValue("threshold")),
		WindowSec:   parseIntForm(r.FormValue("window_sec")),
		CooldownSec: parseIntForm(r.FormValue("cooldown_sec")),
		WebhookURL:  strings.TrimSpace(r.FormValue("webhook_url")),
		Enabled:     r.FormValue("enabled") == "1",
	}
	if a.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	switch a.Type {
	case model.AlertErrorRate, model.AlertP95Latency, model.AlertCostSpike, model.AlertKeyExhausted:
	default:
		http.Error(w, "invalid alert type", http.StatusBadRequest)
		return
	}

	var err error
	if cur, ok := existing.(*model.Alert); ok && cur != nil {
		a.ID = cur.ID
		a.LastFiredAt = cur.LastFiredAt
		a.CreatedAt = cur.CreatedAt
		err = h.store.UpdateAlert(a)
	} else {
		err = h.store.CreateAlert(a)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/alerts", http.StatusSeeOther)
}

// alertAck marks an alert event as acknowledged.
func (h *Handler) alertAck(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := h.store.AckAlertEvent(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func parseFloatForm(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func parseIntForm(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// AnalyticsPage renders the analytics dashboard with time range
// selector, trend chart, and breakdowns by model / channel / token.
func (h *Handler) AnalyticsPage(w http.ResponseWriter, r *http.Request) {
	rangeVal := r.URL.Query().Get("range")
	now := time.Now().UTC()
	var createdFrom int64
	switch rangeVal {
	case "24h":
		createdFrom = now.Add(-24 * time.Hour).Unix()
	case "7d":
		createdFrom = now.Add(-7 * 24 * time.Hour).Unix()
	case "30d":
		createdFrom = now.Add(-30 * 24 * time.Hour).Unix()
	case "90d":
		createdFrom = now.Add(-90 * 24 * time.Hour).Unix()
	}
	filter := logstore.QueryFilter{Limit: 20}
	if createdFrom > 0 {
		filter.CreatedFrom = createdFrom
	}
	stats, _ := h.logStore.Stats(nil)
	if createdFrom > 0 {
		series, _ := h.logStore.TimeSeries(filter, 0, nil)
		var s logstore.LogStatsResult
		for _, b := range series {
			s.Total += b.Requests
			s.Errors += b.Errors
			s.PromptTokens += b.PromptTokens
			s.CompletionTokens += b.CompletionTokens
			s.RealCostUSD += b.RealCostUSD
			s.BilledCostUSD += b.BilledCostUSD
		}
		stats = s
	}

	series, _ := h.logStore.TimeSeries(filter, 3600, nil)
	byModel, _ := h.logStore.TopByField(filter, "model", 20, nil)
	byChannel, _ := h.logStore.TopByField(filter, "channel_id", 20, nil)
	byToken, _ := h.logStore.TopByField(filter, "token_id", 20, nil)

	seriesJSON := "[]"
	if len(series) > 0 {
		b, err := json.Marshal(series)
		if err == nil {
			seriesJSON = string(b)
		}
	}

	data := map[string]any{
		"Body":       "analytics_dashboard_body",
		"Title":      "分析",
		"User":       userToView(getUser(r)),
		"Active":     "analytics",
		"Stats":      stats,
		"ByModel":    byModel,
		"ByChannel":  byChannel,
		"ByToken":    byToken,
		"SeriesJSON": seriesJSON,
		"Range":      rangeVal,
	}
	if err := h.renderer.Render(w, "analytics_dashboard_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ConfigPage renders the YAML config editor.
func (h *Handler) ConfigPage(w http.ResponseWriter, r *http.Request) {
	yaml := readConfigYAML(h.configPath)
	data := map[string]any{
		"Body":       "config_yaml_body",
		"Title":      "配置",
		"User":       userToView(getUser(r)),
		"Active":     "config",
		"ConfigYAML": yaml,
	}
	if err := h.renderer.Render(w, "config_yaml_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ConfigSave handles POST to save the YAML.
func (h *Handler) ConfigSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body := r.FormValue("yaml")
	if err := writeFileAtomic(h.configPath, []byte(body)); err != nil {
		// re-render with error
		data := map[string]any{
			"Body":       "config_yaml_body",
			"Title":      "配置",
			"User":       userToView(getUser(r)),
			"Active":     "config",
			"ConfigYAML": body,
			"FormError":  "保存失败: " + err.Error(),
		}
		_ = h.renderer.Render(w, "config_yaml_body", data)
		return
	}
	h.triggerReload()
	data := map[string]any{
		"Body":       "config_yaml_body",
		"Title":      "配置",
		"User":       userToView(getUser(r)),
		"Active":     "config",
		"ConfigYAML": body,
		"Saved":      true,
	}
	if err := h.renderer.Render(w, "config_yaml_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// EffectivePage renders the effective (runtime) config.
func (h *Handler) EffectivePage(w http.ResponseWriter, r *http.Request) {
	effective, _ := loadEffectiveYAML(h.configPath, h.store)
	data := map[string]any{
		"Body":      "effective_body",
		"Title":     "运行时",
		"User":      userToView(getUser(r)),
		"Active":    "effective",
		"Effective": effective,
	}
	if err := h.renderer.Render(w, "effective_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ---------- helpers ----------

func readConfigYAML(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "# config file not found: " + err.Error()
	}
	return string(data)
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadEffectiveYAML reads the YAML file plus runtime overrides and
// returns a flat key->value map for display.
func loadEffectiveYAML(path string, st interface{ GetRuntimeSettings() ([]byte, error) }) (map[string]string, error) {
	out := map[string]string{}
	if path == "" {
		return out, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	// For the simple display we just show a few of the most relevant
	// fields by default. The full YAML is on the edit page.
	lines := strings.Split(string(data), "\n")
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		if colon := strings.Index(ln, ":"); colon > 0 {
			key := strings.TrimSpace(ln[:colon])
			val := strings.TrimSpace(strings.Trim(strings.TrimSpace(ln[colon+1:]), `"'`))
			if val == "" || val == "|" || val == ">" {
				continue
			}
			if strings.Contains(key, "_") || strings.HasPrefix(key, "server.") {
				out[key] = val
			}
		}
	}
	// Also include runtime_settings overrides
	if raw, err := st.GetRuntimeSettings(); err == nil && len(raw) > 0 {
		out["runtime_settings"] = "(custom - see config dump)"
	}
	_ = filepath.Base // suppress unused
	return out, nil
}
