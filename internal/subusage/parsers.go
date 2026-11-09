package subusage

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// customParser extracts one window from an arbitrary JSON endpoint
// using a dot path. It exists so an operator can wire up a vendor we
// have no built-in parser for without waiting for a release.
//
// JSONPath syntax (deliberately tiny — no library):
//
//	data.quota                      map key
//	data.list.0.used                array index
//	$.data.window.used              leading "$." ignored
//
// The object found at that path is read for the same field families
// the zhipu harvester understands (used / total / remaining / resetAt);
// a plain number at the path is treated as the "used" value with no
// limit (spend-only).
func customParser(ctx context.Context, in Input) (Report, error) {
	if in.EndpointURL == "" {
		return Report{}, fmt.Errorf("custom: endpoint_url is required")
	}
	if in.JSONPath == "" {
		return Report{}, fmt.Errorf("custom: json_path is required")
	}
	headers := map[string]string{}
	if in.APIKey != "" {
		headers["Authorization"] = "Bearer " + in.APIKey
	}
	if in.Cookie != "" {
		headers["Cookie"] = in.Cookie
	}

	var doc any
	raw, err := getJSON(ctx, in.EndpointURL, headers, &doc)
	rep := Report{Raw: raw, Experimental: true}
	if err != nil {
		return rep, err
	}

	node, ok := lookupPath(doc, in.JSONPath)
	if !ok {
		rep.Warn = fmt.Sprintf("json_path %q did not match anything in the response", in.JSONPath)
		return rep, nil
	}

	window := in.WindowName
	if window == "" {
		window = WindowCredits
	}
	unit := in.Unit
	if unit == "" {
		unit = UnitCredits
	}

	point := UsagePoint{Window: window, Scope: "account", Unit: unit, Remaining: -1}
	switch v := node.(type) {
	case map[string]any:
		if p, ok := windowFromObject(v, in.JSONPath); ok {
			point.Used = p.Used
			point.Limit = p.Limit
			point.Remaining = p.Remaining
			point.ResetsAt = p.ResetsAt
			if p.Unit != "" && in.Unit == "" {
				point.Unit = p.Unit
			}
		} else {
			rep.Warn = fmt.Sprintf("json_path %q resolved to an object with no recognised used/total/remaining fields", in.JSONPath)
			return rep, nil
		}
	default:
		if f, ok := asFloat(v); ok {
			point.Used = f
		} else {
			rep.Warn = fmt.Sprintf("json_path %q resolved to %T, not a number or object", in.JSONPath, node)
			return rep, nil
		}
	}
	point.Window = window
	rep.Points = []UsagePoint{point}
	return rep, nil
}

// lookupPath walks a dot path through decoded JSON. Numeric segments
// index arrays; an empty segment or "$" prefix is skipped.
func lookupPath(doc any, path string) (any, bool) {
	path = strings.TrimPrefix(strings.TrimSpace(path), "$")
	path = strings.TrimPrefix(path, ".")
	if path == "" {
		return doc, true
	}
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[seg]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false
			}
			cur = node[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

func init() {
	RegisterParser("openrouter_key", parseOpenRouterKey)
	RegisterParser("zhipu_web", zhipuWebParser)
	RegisterParser("custom", customParser)
}
