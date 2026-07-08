package webui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

const (
	// DefaultSessionTTL is how long an admin session stays valid.
	DefaultSessionTTL = 24 * time.Hour

	// sessionCookieName is the cookie key used to identify a session.
	sessionCookieName = "llmrx_session"
)

type ctxKey string

const userCtxKey ctxKey = "admin_user"

func newSessionToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("webui: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func nowAdd(d time.Duration) time.Time {
	return time.Now().Add(d).UTC()
}

// SessionMiddleware reads llmrx_session cookie, resolves the user
// from store, and attaches it to the request context.
func SessionMiddleware(st store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(sessionCookieName)
			if err != nil {
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}
			u, err := st.GetUserBySession(cookie.Value)
			if err != nil || u == nil {
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}
			if u.SessionExp != nil && time.Now().After(*u.SessionExp) {
				http.Redirect(w, r, "/admin/login?error=session_expired", http.StatusSeeOther)
				return
			}
			ctx := context.WithValue(r.Context(), userCtxKey, u)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func getUser(r *http.Request) *model.User {
	if u, ok := r.Context().Value(userCtxKey).(*model.User); ok {
		return u
	}
	return nil
}

// MethodOverride middleware allows HTML forms (which only support
// GET/POST) to issue PUT/DELETE requests via a hidden _method field.
// Used for form-based edits where HTMX is not appropriate.
func MethodOverride(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			if m := r.FormValue("_method"); m != "" {
				upper := strings.ToUpper(m)
				if upper == "PUT" || upper == "PATCH" || upper == "DELETE" {
					r.Method = upper
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func escapeHTML(s string) string {
	const (
		lt   = "<"
		gt   = ">"
		amp  = "&"
		quot = "\""
		apos = "'"
	)
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '<':
			b = append(b, lt...)
		case '>':
			b = append(b, gt...)
		case '&':
			b = append(b, amp...)
		case '"':
			b = append(b, quot...)
		case '\'':
			b = append(b, apos...)
		default:
			b = append(b, c)
		}
	}
	return string(b)
}

// webAPIBridge is a placeholder for the admin JSON API. It is kept
// here so that the HTML handlers can call the underlying store
// operations directly while preserving a clear migration path
// toward a unified admin API. Currently the bridge only exposes
// store access; future phases can grow it with typed wrappers.
type webAPIBridge struct {
	store    store.Store
	reloader func() error
}

// WebAPIBridge is the exported alias used by server.go to construct
// the webui handler. It currently just wraps a store reference.
type WebAPIBridge = webAPIBridge

// NewWebAPIBridge builds a bridge backed by the given store.
func NewWebAPIBridge(st store.Store) *WebAPIBridge { return &WebAPIBridge{store: st} }

// SetReloader installs a callback that rebuilds in-memory state
// (tokencache, pool, breaker) after writes. Called by server.go
// once the admin handler is wired in.
func (b *WebAPIBridge) SetReloader(fn func() error) { b.reloader = fn }

// TriggerReload invokes the registered reloader (if any).
func (b *WebAPIBridge) TriggerReload() error {
	if b.reloader == nil {
		return nil
	}
	return b.reloader()
}

// Store returns the underlying store. Exposed so the html handlers
// can call store methods directly during the migration window.
func (b *WebAPIBridge) Store() store.Store { return b.store }
