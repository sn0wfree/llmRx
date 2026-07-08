package webui

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

//go:embed templates
var templateFS embed.FS

// Renderer loads all html templates and provides Render/RenderPartial.
type Renderer struct {
	templates *template.Template
}

func NewRenderer() (*Renderer, error) {
	tpl := template.New("")

	r := &Renderer{templates: tpl}
	funcs := template.FuncMap{
		"formatTime": func(t any) string {
			switch v := t.(type) {
			case time.Time:
				if v.IsZero() {
					return "—"
				}
				return v.Format("2006-01-02 15:04")
			case int64:
				if v == 0 {
					return "—"
				}
				return time.Unix(v, 0).Format("2006-01-02 15:04")
			default:
				return "—"
			}
		},
		"formatTimePtr": func(t *time.Time) string {
			if t == nil || t.IsZero() {
				return "—"
			}
			return t.Format("2006-01-02 15:04")
		},
		"statusBadge": func(status any) template.HTML {
			s := 0
			switch v := status.(type) {
			case int:
				s = v
			case int64:
				s = int(v)
			case uint:
				s = int(v)
			case model.ChannelStatus:
				s = int(v)
			case model.TokenStatus:
				s = int(v)
			}
			if s == 1 {
				return `<span class="px-2 py-0.5 text-xs bg-green-100 text-green-800 rounded">启用</span>`
			}
			return `<span class="px-2 py-0.5 text-xs bg-gray-100 text-gray-800 rounded">禁用</span>`
		},
		"truncate": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "..."
		},
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		"joinStrings": func(arr []string) string {
			return strings.Join(arr, "\n")
		},
		"contains": func(arr []string, s string) bool {
			for _, x := range arr {
				if x == s {
					return true
				}
			}
			return false
		},
		"maskKey": func(s string) string {
			if len(s) <= 8 {
				return "***"
			}
			return s[:4] + "..." + s[len(s)-4:]
		},
		// renderPartial executes a named template with the given data
		// and returns the rendered HTML. Used by base.html to dispatch
		// to a page-specific body template.
		"renderPartial": func(name string, data any) (template.HTML, error) {
			var buf bytes.Buffer
			if err := r.templates.ExecuteTemplate(&buf, name, data); err != nil {
				return "", err
			}
			return template.HTML(buf.String()), nil
		},
	}

	t := tpl.Funcs(funcs)

	// Parse all html templates. Go's filepath.Match doesn't support
	// `**` so we list each level explicitly. Patterns that match no
	// files are skipped (ParseFS errors otherwise). We also list
	// files starting with `_` explicitly since `*` does not match
	// them.
	patterns := []string{
		"templates/layouts/*.html",
		"templates/partials/*.html",
		"templates/*.html",
		"templates/*/*.html",
		"templates/*/_*.html",
		"templates/*/*/*.html",
	}
	for _, p := range patterns {
		parsed, err := t.ParseFS(templateFS, p)
		if err != nil {
			if strings.Contains(err.Error(), "matches no files") {
				continue
			}
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		t = parsed
	}

	r.templates = t
	return r, nil
}

// Render a full page (includes base layout). The page template must
// define a block with the same name as the page argument.
func (r *Renderer) Render(w io.Writer, page string, data any) error {
	return r.templates.ExecuteTemplate(w, "layouts/base.html", data)
}

// RenderPartial renders a single template without the base layout.
// Useful for HTMX partial updates.
func (r *Renderer) RenderPartial(w io.Writer, name string, data any) error {
	return r.templates.ExecuteTemplate(w, name, data)
}

// Flash writes an HTMX OOB flash message to the response.
func (r *Renderer) Flash(w http.ResponseWriter, level, msg string) {
	colors := map[string]string{
		"success": "green",
		"error":   "red",
		"warning": "yellow",
		"info":    "blue",
	}
	c := colors[level]
	if c == "" {
		c = "blue"
	}
	html := fmt.Sprintf(
		`<div id="flash" hx-swap-oob="innerHTML" class="p-3 rounded bg-%s-100 text-%s-800 border border-%s-200">%s</div>`,
		c, c, c, template.HTMLEscapeString(msg),
	)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(html))
}

// PageData wraps data with common fields needed by base.html.
type PageData struct {
	Title   string
	User    *User
	Active  string
	Flash   string
	Data    any
}

// User is a minimal projection of model.User for templates.
type User struct {
	ID       int64
	Username string
	Role     int
}
