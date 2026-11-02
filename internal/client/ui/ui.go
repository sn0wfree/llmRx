// Package ui serves the edge client's local status page.
//
// It is deliberately dependency-free: no CDN, no HTMX, no build step.
// The client exists so agents keep working when the network is down, so
// its own UI must remain usable in exactly that situation — a page that
// needs cdn.tailwindcss.com would render blank offline. Styles are
// inlined; every asset comes from go:embed.
//
// The page is server-rendered on each request. There are three
// actions (refresh, purge cache, save settings), handled as plain form
// POSTs with 303 redirects so the page works without JavaScript.
package ui

import (
	"embed"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/sn0wfree/llmRx/internal/cache"
	"github.com/sn0wfree/llmRx/internal/client"
)

//go:embed templates/*.html
var templateFS embed.FS

// Data is everything the page renders.
type Data struct {
	// Status
	Version       string
	Listen        string
	Servers       []string
	Uptime        string
	BreakerOpen   bool
	BreakerFails  int
	Connectivity  string // "ok" | "degraded" | "offline"
	ConfigVersion int
	ControlErr    string
	ControlOK     string

	// Cache
	CacheEnabled bool
	CacheSize    int64
	CacheHits    int64
	CacheMisses  int64
	CacheHitRate string

	// Usage
	PendingUsage int
	UsageFile    string

	// Settings (masked when displayed)
	ServerList   string
	TokenMasked  string
	ClientMasked string
	ClientName   string
	CacheTTLSec  int
	CacheMax     int
	FallbackMode string
	UsageURL     string

	// Flash message after an action.
	Flash string
	Error string
}

// Server wires the UI to the proxy and the config file it edits.
type Server struct {
	proxy   *client.Proxy
	cfg     *client.Config
	cfgPath string
	version string
	tmpl    *template.Template
	// saveConfig writes the config to disk; injected so tests can
	// capture the write instead of touching the filesystem.
	saveConfig func(*client.Config) error
}

// New builds the UI handler. cfgPath is the file the settings form
// writes back to.
func New(p *client.Proxy, cfg *client.Config, cfgPath, version string) (*Server, error) {
	tmpl, err := template.New("ui").Funcs(template.FuncMap{
		"mask": maskSecret,
		// Connectivity is rendered as a coloured dot plus a label; the
		// template only carries the state string.
		"connDot": func(s string) string {
			switch s {
			case "ok":
				return "ok"
			case "degraded":
				return "warn"
			default:
				return "bad"
			}
		},
		"connText": func(s string) string {
			switch s {
			case "ok":
				return "服务端可达"
			case "degraded":
				return "服务端不稳定"
			default:
				return "服务端不可达"
			}
		},
	}).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{
		proxy:   p,
		cfg:     cfg,
		cfgPath: cfgPath,
		version: version,
		tmpl:    tmpl,
		saveConfig: func(c *client.Config) error {
			return client.WriteConfigFile(cfgPath, c)
		},
	}, nil
}

// Handler returns the /ui routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ui", s.handleIndex)
	mux.HandleFunc("/ui/", s.handleIndex)
	mux.HandleFunc("/ui/cache/purge", s.handlePurge)
	mux.HandleFunc("/ui/settings", s.handleSettings)
	return mux
}

// collect gathers the render model. Every value comes from data the
// proxy already maintains — the UI adds no instrumentation of its own.
func (s *Server) collect(r *http.Request, flash, errMsg string) Data {
	st := s.proxy.Cache().Stats(r.Context())
	open, fails := s.proxy.ServerBreaker()
	cfg := s.cfg

	connectivity := "ok"
	switch {
	case open:
		connectivity = "offline"
	case fails > 0:
		connectivity = "degraded"
	}

	servers := cfg.ServersSnapshot()
	d := Data{
		Version:      s.version,
		Listen:       cfg.Listen,
		Servers:      servers,
		Uptime:       s.proxy.Uptime().Truncate(time.Second).String(),
		BreakerOpen:  open,
		BreakerFails: fails,
		Connectivity: connectivity,

		CacheEnabled: cfg.CacheEnabled,
		CacheSize:    st.Size,
		CacheHits:    st.Hits,
		CacheMisses:  st.Misses,
		CacheHitRate: formatRate(st),

		PendingUsage: s.proxy.PendingUsage(),
		UsageFile:    cfg.UsageFile,

		ServerList:   strings.Join(servers, "\n"),
		TokenMasked:  maskSecret(cfg.Token),
		ClientMasked: maskSecret(cfg.ClientToken),
		ClientName:   cfg.ClientName,
		CacheTTLSec:  cfg.CacheTTLSec,
		CacheMax:     cfg.CacheMaxItems,
		FallbackMode: cfg.FallbackModeOrDefault(),
		UsageURL:     cfg.UsageEndpoint,

		Flash: flash,
		Error: errMsg,
	}
	if ctl := s.proxy.Control(); ctl != nil {
		d.ConfigVersion = ctl.AppliedVersion()
		lastErr, lastOK := ctl.LastStatus()
		d.ControlErr = lastErr
		if !lastOK.IsZero() {
			d.ControlOK = lastOK.UTC().Format("2006-01-02 15:04:05 UTC")
		}
	}
	return d
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.render(w, s.collect(r, r.URL.Query().Get("flash"), r.URL.Query().Get("error")))
}

func (s *Server) render(w http.ResponseWriter, d Data) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The page shows local-only state; never let a proxy cache it.
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.ExecuteTemplate(w, "page", d); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handlePurge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.proxy.Cache().Purge(r.Context()); err != nil {
		http.Redirect(w, r, "/ui?error="+urlEncode(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/ui?flash="+urlEncode("缓存已清空"), http.StatusSeeOther)
}

// handleSettings persists the form back to client.yml.
//
// Only fields the form actually shows are writable. Tokens are NOT
// editable here: the page masks them, so a round-trip would write the
// mask back over the real secret. Rotating a token is `-enroll` (or a
// manual file edit), which is intentional — a settings form is the
// wrong place to overwrite a credential by accident.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/ui?error="+urlEncode("表单解析失败"), http.StatusSeeOther)
		return
	}

	cfg := s.cfg
	mu := cfg.Mu()
	mu.Lock()
	if v := strings.TrimSpace(r.FormValue("servers")); v != "" {
		var servers []string
		for _, line := range strings.Split(v, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				servers = append(servers, line)
			}
		}
		if len(servers) > 0 {
			cfg.Servers = servers
		}
	}
	cfg.CacheEnabled = r.FormValue("cache_enabled") == "on"
	if v := parseInt(r.FormValue("cache_ttl_sec")); v > 0 {
		cfg.CacheTTLSec = v
	}
	if v := parseInt(r.FormValue("cache_max_items")); v > 0 {
		cfg.CacheMaxItems = v
	}
	if v := r.FormValue("fallback_mode"); v == "local" || v == "cache_then_fail" {
		cfg.FallbackMode = v
	}
	mu.Unlock()

	if err := s.saveConfig(cfg); err != nil {
		http.Redirect(w, r, "/ui?error="+urlEncode("保存失败: "+err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/ui?flash="+urlEncode("设置已保存"), http.StatusSeeOther)
}

// ---------- helpers ----------

// maskSecret renders a credential with only its head and tail visible.
// Mirrors the server's secrets.Mask so operators see a familiar shape.
func maskSecret(s string) string {
	if s == "" {
		return "(未设置)"
	}
	if len(s) <= 10 {
		return "***"
	}
	return s[:5] + "***" + s[len(s)-4:]
}

func formatRate(st cache.Stats) string {
	total := st.Hits + st.Misses
	if total == 0 {
		return "—"
	}
	// Integer math keeps this allocation-free on the render path.
	pct := st.Hits * 1000 / total
	return itoa(pct/10) + "." + itoa(pct%10) + "%"
}

func parseInt(s string) int {
	const limit = 1_000_000_000
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
		// Clamp rather than overflow: these values come from a form and
		// an absurd entry must not wrap into a small number.
		if n > limit {
			return limit
		}
	}
	return n
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func urlEncode(s string) string {
	return template.URLQueryEscaper(s)
}
