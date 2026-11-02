package webui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
)

// EdgeControl is the narrow surface the Clients page needs. Declared
// here so internal/webui does not import internal/clientapi (which
// depends on the log store, and thus CGO).
type EdgeControl interface {
	EdgeClientsStatusJSON() any
	CurrentBundleJSON() any
	PushBundleJSON(raw json.RawMessage) (int, error)
	IssueEnrollTokenJSON(clientName string) (any, error)
	RevokeEdgeClientJSON(clientID string) error
}

// SetEdgeControl injects the edge-client control plane.
func (h *Handler) SetEdgeControl(e EdgeControl) { h.edgeControl = e }

// clientView is one row on the page.
type clientView struct {
	Name          string
	ClientID      string
	Online        bool
	Revoked       bool
	ClientVersion string
	LastSeen      string
	LastSeenTitle string
}

// ClientsPage renders the edge-client management page: the client list,
// enrollment-code issuance, and the config-bundle editor.
func (h *Handler) ClientsPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Body":    "clients_body",
		"Title":   "客户端",
		"User":    userToView(getUser(r)),
		"Active":  "clients",
		"Flash":   r.URL.Query().Get("flash"),
		"Error":   r.URL.Query().Get("error"),
		"Enabled": h.edgeControl != nil,
	}

	if h.edgeControl != nil {
		data["Clients"] = h.clientViews()
		bundle, _ := json.MarshalIndent(h.edgeControl.CurrentBundleJSON(), "", "  ")
		data["BundleJSON"] = string(bundle)
	}
	if tok := r.URL.Query().Get("enroll_token"); tok != "" {
		data["NewToken"] = tok
		data["NewTokenExpires"] = r.URL.Query().Get("expires_at")
	}

	if err := h.renderer.Render(w, "clients_body", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// clientViews converts the control plane's status payload into rows.
func (h *Handler) clientViews() []clientView {
	raw := h.edgeControl.EdgeClientsStatusJSON()
	list, ok := raw.([]map[string]any)
	if !ok {
		// The ingestor returns a typed slice; round-trip through JSON so
		// this stays decoupled from its struct definition.
		b, err := json.Marshal(raw)
		if err != nil {
			return nil
		}
		_ = json.Unmarshal(b, &list)
	}
	out := make([]clientView, 0, len(list))
	for _, m := range list {
		v := clientView{
			Name:     str(m["name"]),
			ClientID: str(m["client_id"]),
			Online:   boolVal(m["online"]),
			Revoked:  boolVal(m["revoked"]),
		}
		v.ClientVersion = str(m["client_version"])
		if v.Revoked {
			// A revoked client is never "online", even if it checked in
			// recently: the credential no longer works.
			v.Online = false
			v.LastSeen = "—"
		} else if ls := str(m["last_seen"]); ls != "" {
			v.LastSeen = humanTime(ls)
			v.LastSeenTitle = ls
		} else {
			v.LastSeen = "从未"
		}
		out = append(out, v)
	}
	return out
}

// ClientsEnroll issues a code and redirects with it in the query string
// so the page can display it once.
//
// The token travels in a redirect URL, which lands in browser history.
// That is acceptable for a 15-minute single-use code and much simpler
// than a flash store; the page labels it clearly.
func (h *Handler) ClientsEnroll(w http.ResponseWriter, r *http.Request) {
	if h.edgeControl == nil {
		http.Redirect(w, r, "/admin/clients?error="+urlEncode("未配置客户端"), http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/clients?error="+urlEncode("表单解析失败"), http.StatusSeeOther)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	out, err := h.edgeControl.IssueEnrollTokenJSON(name)
	if err != nil {
		http.Redirect(w, r, "/admin/clients?error="+urlEncode(err.Error()), http.StatusSeeOther)
		return
	}
	m, _ := out.(map[string]any)
	tok, _ := m["token"].(string)
	exp, _ := m["expires_at"].(string)
	http.Redirect(w, r, "/admin/clients?enroll_token="+urlEncode(tok)+
		"&expires_at="+urlEncode(exp)+
		"&flash="+urlEncode("接入码已生成，只显示这一次"), http.StatusSeeOther)
}

// ClientsRevoke disables an enrolled client.
func (h *Handler) ClientsRevoke(w http.ResponseWriter, r *http.Request) {
	if h.edgeControl == nil {
		http.Redirect(w, r, "/admin/clients?error="+urlEncode("未配置客户端"), http.StatusSeeOther)
		return
	}
	id := chiURLParam(r, "id")
	if err := h.edgeControl.RevokeEdgeClientJSON(id); err != nil {
		http.Redirect(w, r, "/admin/clients?error="+urlEncode(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/clients?flash="+urlEncode("已吊销 "+id), http.StatusSeeOther)
}

// ClientsPushConfig replaces the config bundle.
func (h *Handler) ClientsPushConfig(w http.ResponseWriter, r *http.Request) {
	if h.edgeControl == nil {
		http.Redirect(w, r, "/admin/clients?error="+urlEncode("未配置客户端"), http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/clients?error="+urlEncode("表单解析失败"), http.StatusSeeOther)
		return
	}
	raw := strings.TrimSpace(r.FormValue("bundle"))
	if raw == "" {
		http.Redirect(w, r, "/admin/clients?error="+urlEncode("配置内容为空"), http.StatusSeeOther)
		return
	}
	version, err := h.edgeControl.PushBundleJSON(json.RawMessage(raw))
	if err != nil {
		http.Redirect(w, r, "/admin/clients?error="+urlEncode("推送失败: "+err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/clients?flash="+urlEncode("配置已推送，版本 "+itoaSimple(int64(version))), http.StatusSeeOther)
}

// ---------- small helpers ----------

func str(v any) string {
	s, _ := v.(string)
	return s
}

// urlEncode escapes a value for a redirect query string.
func urlEncode(s string) string { return url.QueryEscape(s) }

// chiURLParam reads a URL parameter from the chi route context.
func chiURLParam(r *http.Request, key string) string { return chi.URLParam(r, key) }

func boolVal(v any) bool {
	b, _ := v.(bool)
	return b
}

// itoaSimple avoids pulling strconv in for one call site.
func itoaSimple(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// humanTime renders an RFC3339 timestamp as a short local-looking label,
// falling back to the raw string if it does not parse.
func humanTime(s string) string {
	if len(s) >= 19 {
		// "2006-01-02T15:04:05Z" → "01-02 15:04"
		return s[5:10] + " " + s[11:16]
	}
	return s
}
