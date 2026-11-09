package api

import (
	"context"
	"net"
	"strings"

	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/router/auto"
	"github.com/sn0wfree/llmRx/internal/router/session"
	"github.com/sn0wfree/llmRx/internal/store"
	"net/http"
)

// lookupTokenInfo extracts the TokenInfo placed in the request
// context by middleware.Token. Returns ok=false when the request
// was authenticated without a TokenInfo in context (some unit tests
// bypass the middleware by going directly through a Handler method).
func lookupTokenInfo(ctx context.Context) (middleware.TokenInfo, bool) {
	v, ok := ctx.Value(middleware.TokenInfoKey).(middleware.TokenInfo)
	return v, ok
}

// findDefaultCombo returns the "default" combo for a token, used by
// the model="auto" shortcut. Priority:
//  1. combo with IsDefault=true and Enabled
//  2. combo named "auto" and Enabled
//  3. first enabled combo (by map iteration order, non-deterministic)
//
// Returns ok=false when no enabled combo exists.
func findDefaultCombo(info middleware.TokenInfo) (model.TokenComboModel, bool) {
	var namedAuto, first model.TokenComboModel
	var hasNamedAuto, hasFirst bool
	for name, c := range info.ComboModels {
		if !c.Enabled {
			continue
		}
		if c.IsDefault {
			return c, true
		}
		if name == "auto" {
			namedAuto = c
			hasNamedAuto = true
			continue
		}
		if !hasFirst {
			first = c
			hasFirst = true
		}
	}
	if hasNamedAuto {
		return namedAuto, true
	}
	return first, hasFirst
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// resolveTask determines the task type for task-aware routing.
// task_source=header reads X-Task-Type from the request; task_source
// =auto runs the built-in keyword classifier over the prompt plus
// recent context; anything else returns "" (feature off).
func (h *Handler) resolveTask(r *http.Request, req *provider.ChatRequest, text string) (string, string) {
	switch h.taskSource {
	case "header":
		if t := r.Header.Get("X-Task-Type"); t != "" && auto.ValidTask(t) {
			return t, "header"
		}
		return "", ""
	case "auto":
		ctx := auto.TaskContextText(text, messageTexts(req.Messages), 3)
		return string(h.taskClassifier.Classify(ctx)), "auto"
	default:
		return "", ""
	}
}

// messageTexts flattens request messages into plain text tails for
// task classification context.
func messageTexts(msgs []provider.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.ContentString())
	}
	return out
}

// sessionIDFor derives the session key for one request, in
// priority order:
//
//  1. the X-Session-ID header (explicit client control)
//  2. the request's user field (OpenAI passthrough)
//  3. a hash of (system prompt + first user message) — gives
//     multi-turn affinity without any client cooperation
//
// Returns "" when sessions are not configured.
func (h *Handler) sessionIDFor(r *http.Request, req *provider.ChatRequest) string {
	if h.sessions == nil {
		return ""
	}
	if id := strings.TrimSpace(r.Header.Get("X-Session-ID")); id != "" {
		return "header:" + id
	}
	return sessionIDFromReq(req)
}

// sessionIDFromReq derives the session key from the request alone
// (no header access): explicit user field, else content hash.
func sessionIDFromReq(req *provider.ChatRequest) string {
	if id := strings.TrimSpace(req.User); id != "" {
		return "user:" + id
	}
	system, firstUser := firstTurnText(req.Messages)
	return session.Hash(system, firstUser)
}

// firstTurnText extracts the system prompt and the first user
// message for the session hash.
func firstTurnText(msgs []provider.Message) (system, firstUser string) {
	for _, m := range msgs {
		switch m.Role {
		case "system":
			if system == "" {
				system = contentString(m)
			}
		case "user":
			if firstUser == "" {
				firstUser = contentString(m)
			}
		}
	}
	return system, firstUser
}

// contentString renders a message's content (string or parts) as
// plain text for hashing.
func contentString(m provider.Message) string {
	switch c := m.Content.(type) {
	case string:
		return c
	case []provider.ContentPart:
		var sb strings.Builder
		for _, p := range c {
			if p.Text != "" {
				sb.WriteString(p.Text)
			}
		}
		return sb.String()
	default:
		return ""
	}
}

// clientIP returns the best-effort source IP for the request,
// consulting proxy headers only when the operator has opted
// in AND the immediate peer is in the trusted proxy CIDR list.
//
// Defaults to r.RemoteAddr so a direct-internet deployment
// can't be spoofed into bypassing per-token IP whitelists via
// X-Forwarded-For.
func (h *Handler) clientIP(r *http.Request) string {
	if h.cfg != nil && h.cfg.Server.TrustProxyHeaders {
		if v := h.firstTrustedProxyHeader(r); v != "" {
			return v
		}
	}
	// Normalize to the bare host: r.RemoteAddr is "ip:port", but
	// consumers (per-token IP whitelists, the registration throttle)
	// match/aggregate on bare IPs. Keying on the ephemeral source
	// port would let a client dodge per-IP limits and break exact
	// whitelist matches, so strip it.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

// firstTrustedProxyHeader returns the leftmost X-Forwarded-For
// or X-Real-IP value when r.RemoteAddr is in the configured
// trusted CIDR list. Returns "" otherwise.
func (h *Handler) firstTrustedProxyHeader(r *http.Request) string {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = r.RemoteAddr
	}
	if !h.peerIsTrustedProxy(remoteHost) {
		return ""
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		// XFF is a comma-separated chain leftmost=client;
		// trim spaces and return the leftmost entry.
		if i := strings.IndexByte(v, ','); i >= 0 {
			v = v[:i]
		}
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

// peerIsTrustedProxy reports whether the configured CIDR list
// trusts the immediate peer. An empty list under TrustProxyHeaders
// is treated as "trust every source" so the operator can opt in
// for tightly-controlled environments where every peer is a
// reverse proxy (e.g. K8s behind a single service mesh).
func (h *Handler) peerIsTrustedProxy(remoteHost string) bool {
	if len(h.cfg.Server.TrustedProxyCIDRs) == 0 {
		return true
	}
	ip := net.ParseIP(remoteHost)
	if ip == nil {
		return false
	}
	for _, cidr := range h.cfg.Server.TrustedProxyCIDRs {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func lookupTokenID(ctx context.Context, st store.Store) int64 {
	v, ok := ctx.Value(middleware.TokenIDKey).(int64)
	if !ok {
		return 0
	}
	return v
}

// lastUserText returns the last user-role message in the conversation
// for L4 intent classification. If no user message is present, the
// empty string is returned, which disables L4.
func lastUserText(msgs []provider.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].ContentString()
		}
	}
	return ""
}
