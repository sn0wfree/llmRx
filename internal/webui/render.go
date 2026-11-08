package webui

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

//go:embed templates
var templateFS embed.FS

// jstr escapes s as a double-quoted JS string literal so it can be
// interpolated into hx-confirm (or any JS-eval'd) attribute safely.
func jstr(s string) string {
	return strconv.Quote(s)
}

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
		// providerSupportsBalance lets the channels template show "—"
		// for providers with no balance endpoint (P14).
		"providerSupportsBalance": providerSupportsBalance,
		// getFormValue reads a form-data map key, tolerating a nil map
		// (the create form has no FormData). Templates cannot index a
		// nil map with a missing key without panicking, and reaching
		// into a nil *model.Channel errors out the whole page.
		"getFormValue": func(m map[string]string, key string) string {
			if m == nil {
				return ""
			}
			return m[key]
		},
		// balanceThresholdStr renders the low-balance line for the
		// channel form: "" for 0 so the placeholder shows instead of a
		// misleading "0.0000" (0 means "not monitored").
		"balanceThresholdStr": func(v float64) string {
			if v <= 0 {
				return ""
			}
			return strconv.FormatFloat(v, 'f', -1, 64)
		},
		// P21 monitoring helpers.
		"mul100": func(v float64) float64 { return v * 100 },
		// min100 clamps a ratio to 0..100 (percent) for bar widths.
		"min100": func(v float64) float64 {
			p := v * 100
			if p > 100 {
				p = 100
			}
			if p < 0 {
				p = 0
			}
			return p
		},
		// fmtQuotaUsed renders a quota quantity in its metric's unit.
		"fmtQuotaUsed": func(metric string, v float64) string {
			switch metric {
			case "usd":
				return "$" + strconv.FormatFloat(v, 'f', 4, 64)
			default:
				return strconv.FormatInt(int64(v), 10) + " tok"
			}
		},
		// fmtWindow renders the quota window label.
		"fmtWindow": func(window string, hours int) string {
			switch window {
			case "monthly":
				return "月窗口"
			case "daily":
				return "日窗口"
			case "rolling":
				return "滚动 " + strconv.Itoa(hours) + "h"
			default:
				return window
			}
		},
		// balanceCell renders the upstream balance column (P14).
		// Three distinct states matter to an operator: a value, a
		// value with a stale-warning, and "not supported".
		"balanceCell": func(ch model.Channel) template.HTML {
			if !providerSupportsBalance(ch.Provider) {
				return template.HTML(`<span class="text-xs text-gray-400" title="该 provider 无公开余额接口">—</span>`)
			}
			if ch.Balance == "" {
				if ch.BalanceError != "" {
					return template.HTML(`<span class="text-xs text-red-600" title="` +
						template.HTMLEscapeString(ch.BalanceError) + `">查询失败</span>`)
				}
				return template.HTML(`<span class="text-xs text-gray-400">未查询</span>`)
			}
			cur := template.HTMLEscapeString(ch.BalanceCurrency)
			label := template.HTMLEscapeString(ch.Balance)
			if cur != "" {
				label += " " + cur
			}
			// A stored error next to a value means the last refresh
			// failed but an older number is still known.
			if ch.BalanceError != "" {
				return template.HTML(`<span class="text-sm text-gray-700" title="上次刷新失败：` +
					template.HTMLEscapeString(ch.BalanceError) + `">` + label +
					` <span class="text-xs text-amber-600">(旧)</span></span>`)
			}
			title := ""
			if ch.BalanceUpdatedAt != nil {
				title = ` title="更新于 ` + template.HTMLEscapeString(ch.BalanceUpdatedAt.UTC().Format("2006-01-02 15:04")) + `"`
			}
			return template.HTML(`<span class="text-sm text-gray-700"` + title + `>` + label + `</span>`)
		},
		"truncate": func(s string, n int) string {			r := []rune(s)
			if len(r) <= n {
				return s
			}
			return string(r[:n]) + "..."
		},
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		// percentage returns the percent used of budget (0..100).
		"percentage": func(used, budget float64) float64 {
			if budget <= 0 {
				return 0
			}
			p := used / budget * 100
			if p > 100 {
				return 100
			}
			return p
		},
		// now returns the current unix seconds (template compare).
		"now": func() int64 { return time.Now().Unix() },
		"joinStrings": func(arr []string) string {
			return strings.Join(arr, "\n")
		},
		// jstr escapes a value as a double-quoted JS string literal
		// (strconv.Quote). hx-confirm values are eval'd by htmx as
		// JS expressions, so unescaped user names (channel/token/
		// plan/alert/username) in the confirm text were a stored XSS
		// vector. html/template escapes the attribute, then jstr
		// makes the interpolated fragment a plain string literal —
		// the pair turns the expression into inert data.
		"jstr": jstr,
		// fieldError returns the error message for a form field, or
		// "" if the field is valid. Used by templates to render
		// inline field-level error text. Renamed from "map" to avoid
		// colliding with the common template helper.
		"fieldError": func(fieldErrors any, key string) string {
			mm, ok := fieldErrors.(map[string]string)
			if !ok {
				return ""
			}
			return mm[key]
		},
		// dict builds a map[string]any from alternating key/value
		// pairs so partial templates can be invoked with a custom
		// data scope. Used by form_error / field_error partials.
		"dict": func(values ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(values); i += 2 {
				k, ok := values[i].(string)
				if !ok {
					continue
				}
				m[k] = values[i+1]
			}
			return m
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

// User is a minimal projection of model.User for templates.
type User struct {
	ID       int64
	Username string
	Role     int
}
