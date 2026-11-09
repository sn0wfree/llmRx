package subusage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// zhipuWeb fetches the GLM Coding Plan usage endpoints.
//
// EXPERIMENTAL. 智谱 does not publish a usage API — the coding-plan
// documentation only points at the web dashboard
// (bigmodel.cn/coding-plan/personal/usage), and that dashboard is a
// SPA whose internal endpoints are not documented.
//
// Verified by inspecting the dashboard's own JS bundles (the
// `claude-usage` chunk lists them):
//
//	GET https://bigmodel.cn/api/paas/v4/monitor/usage/quota/limit
//	GET https://bigmodel.cn/api/paas/v4/monitor/credit-usage/activity
//	GET https://bigmodel.cn/api/paas/v4/monitor/token-usage/activity
//	GET https://bigmodel.cn/api/paas/v4/monitor/token-usage/usage-detail
//
// Authentication is an `Authorization: Bearer <token>` header — a
// session cookie alone is NOT accepted (the API answers "Header中未收到
// Authorization参数"). The token is normally the channel's own API
// key, so the probe prefers the channel key and only falls back to
// the cookie for dashboards that need one.
//
// Because the response shape is undocumented, we walk the decoded
// tree looking for the field families 智谱 uses (used / total /
// quota / remaining / resetTime). A dashboard redesign can break this
// at any time — that is why Report.Experimental is set and why
// failures report the raw-body excerpt.
const zhipuDefaultEndpoint = "https://bigmodel.cn/api/paas/v4/monitor/usage/quota/limit"

func zhipuWebParser(ctx context.Context, in Input) (Report, error) {
	if in.APIKey == "" && in.Cookie == "" {
		return Report{}, fmt.Errorf("zhipu_web: needs a channel API key (Authorization: Bearer) or a session cookie")
	}
	url := in.EndpointURL
	if url == "" {
		url = zhipuDefaultEndpoint
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Report{}, err
	}
	// The dashboard calls these with a JSON accept header and the SPA
	// origin as referer; both are cheap to mimic.
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", "https://bigmodel.cn/coding-plan/personal/usage")
	req.Header.Set("Origin", "https://bigmodel.cn")
	req.Header.Set("User-Agent", "llmRx/usage-probe (+https://github.com/sn0wfree/llmRx)")
	if in.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+in.APIKey)
	}
	if in.Cookie != "" {
		req.Header.Set("Cookie", in.Cookie)
	}

	resp, err := httpDoer().Do(req)
	if err != nil {
		return Report{}, fmt.Errorf("zhipu_web: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Report{}, err
	}
	raw := truncateRaw(body)
	rep := Report{Experimental: true, Raw: raw}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return rep, fmt.Errorf("zhipu_web: rejected (status %d) — the endpoint wants an Authorization: Bearer token; "+
			"check that the channel key is a coding-plan key and that it can reach %s", resp.StatusCode, url)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return rep, fmt.Errorf("zhipu_web: upstream status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	// Decode JSON directly, else try to find an embedded JSON object.
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		if embedded, ok := extractEmbeddedJSON(string(body)); ok {
			doc = embedded
		} else {
			rep.Warn = "response was not JSON and no embedded JSON payload was found; " +
				"the dashboard likely changed — point usage_probe.endpoint_url at the new request, or use the custom parser"
			return rep, nil
		}
	}

	points := harvestZhipuWindows(doc)
	if len(points) == 0 {
		rep.Warn = "no usage fields matched the known patterns; the dashboard response shape changed"
		return rep, nil
	}
	rep.Points = points
	return rep, nil
}

// harvestZhipuWindows walks an arbitrary decoded JSON tree and pulls
// out anything that looks like a quota window. The dashboard nests
// these differently per release, so we look at every object.
func harvestZhipuWindows(doc any) []UsagePoint {
	var out []UsagePoint
	var walk func(node any, path string)
	walk = func(node any, path string) {
		switch v := node.(type) {
		case map[string]any:
			if p, ok := windowFromObject(v, path); ok {
				out = append(out, p)
			}
			// Deterministic traversal order keeps output stable for
			// tests and diffing.
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(v[k], path+"."+k)
			}
		case []any:
			for i := range v {
				walk(v[i], fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
	walk(doc, "")
	return out
}

// windowFromObject recognises the quota field families we have seen.
// It requires at least one numeric quantity so ordinary objects are
// skipped.
func windowFromObject(m map[string]any, path string) (UsagePoint, bool) {
	pick := func(names ...string) (float64, bool) {
		for _, n := range names {
			if v, ok := m[n]; ok {
				if f, ok := asFloat(v); ok {
					return f, true
				}
			}
		}
		return 0, false
	}

	used, hasUsed := pick("used", "usedCredits", "usedQuota", "usedAmount",
		"consumed", "consumedCredits", "usedTokens", "used_tokens", "usage")
	total, hasTotal := pick("total", "totalCredits", "totalQuota", "quota",
		"limit", "totalTokens", "total_tokens", "totalAmount")
	remaining, hasRem := pick("remaining", "remainingCredits", "remainingQuota",
		"left", "available", "remain")
	if !hasUsed && !hasTotal && !hasRem {
		return UsagePoint{}, false
	}
	// A lone "usage" number inside an unrelated object is common; demand
	// a limit or a remaining figure to call it a window.
	if !hasTotal && !hasRem {
		return UsagePoint{}, false
	}

	p := UsagePoint{
		Window:    windowForPath(path),
		Scope:     "plan",
		Used:      used,
		Remaining: -1,
		Unit:      unitForPath(m),
		Label:     lastPathSegment(path),
	}
	if hasTotal {
		p.Limit = total
	}
	if hasRem {
		p.Remaining = remaining
	}
	if reset, ok := pickReset(m); ok {
		p.ResetsAt = reset
	}
	return p, true
}

// windowForPath guesses the reset cadence from the object's path
// (e.g. "...fiveHourQuota..." or "...weekly..."). Falls back to the
// rolling 5-hour window because that is the coding plan's primary cap.
func windowForPath(path string) string {
	l := strings.ToLower(path)
	switch {
	case strings.Contains(l, "5h"), strings.Contains(l, "5hour"), strings.Contains(l, "fivehour"),
		strings.Contains(l, "rolling"), strings.Contains(l, "session"):
		return WindowRolling
	case strings.Contains(l, "week"):
		return WindowWeekly
	case strings.Contains(l, "month"):
		return WindowMonthly
	case strings.Contains(l, "day"), strings.Contains(l, "daily"):
		return WindowDaily
	}
	return WindowRolling
}

func unitForPath(m map[string]any) string {
	if _, ok := m["usedTokens"]; ok {
		return UnitTokens
	}
	if _, ok := m["used_tokens"]; ok {
		return UnitTokens
	}
	if _, ok := m["usedCredits"]; ok {
		return UnitCredits
	}
	if _, ok := m["usedQuota"]; ok {
		return UnitCredits
	}
	return UnitCredits
}

// pickReset reads the many spellings of a reset timestamp.
func pickReset(m map[string]any) (time.Time, bool) {
	for _, k := range []string{"resetAt", "reset_at", "resetTime", "reset_time",
		"refreshAt", "expireAt", "expiresAt", "nextResetAt"} {
		v, ok := m[k]
		if !ok {
			continue
		}
		if t, ok := asTime(v); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

func lastPathSegment(path string) string {
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		return path[i+1:]
	}
	return path
}

// asFloat accepts JSON numbers and quoted numbers (several Chinese
// vendors stringify decimals to preserve precision).
func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		// strconv.ParseFloat, not Sscanf("%g"): Sscanf happily parses
		// the leading "2026" out of an RFC3339 timestamp and returns
		// success, which would make asTime treat the string as a unix
		// second value and drop it.
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	}
	return 0, false
}

// asTime accepts RFC3339 strings, unix seconds, and unix milliseconds.
func asTime(v any) (time.Time, bool) {
	if f, ok := asFloat(v); ok {
		// Heuristic: values past ~2001 in milliseconds are far larger
		// than any sane second timestamp.
		if f > 1e11 {
			return time.UnixMilli(int64(f)).UTC(), true
		}
		if f > 1e9 {
			return time.Unix(int64(f), 0).UTC(), true
		}
		return time.Time{}, false
	}
	if s, ok := v.(string); ok {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.UTC(), true
		}
		if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// extractEmbeddedJSON digs a JSON object out of an HTML page, which is
// how Next.js/SPA dashboards usually ship their server state.
func extractEmbeddedJSON(html string) (any, bool) {
	for _, marker := range []string{"__NEXT_DATA__", "__INITIAL_STATE__", "window.__NUXT__"} {
		idx := strings.Index(html, marker)
		if idx < 0 {
			continue
		}
		start := strings.IndexByte(html[idx:], '{')
		if start < 0 {
			continue
		}
		start += idx
		if doc, ok := decodeBalancedObject(html[start:]); ok {
			return doc, true
		}
	}
	return nil, false
}

// decodeBalancedObject parses the longest balanced {...} prefix,
// ignoring braces inside strings.
func decodeBalancedObject(s string) (any, bool) {
	depth := 0
	inStr := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				var doc any
				if err := json.Unmarshal([]byte(s[:i+1]), &doc); err == nil {
					return doc, true
				}
				return nil, false
			}
		}
	}
	return nil, false
}
