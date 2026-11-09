package subusage

import (
	"context"
	"strings"
	"time"
)

// OpenRouter exposes the richest subscription surface of any vendor we
// integrate: GET /api/v1/key returns both a key-level limit and a list
// of budgets with their own reset intervals. That maps almost 1:1 onto
// our window model, so this parser needs no guessing.
//
// Confirmed response shape (docs: openrouter.ai/docs/api-reference/limits):
//
//	{"data":{
//	  "label":"...", "limit":100.0|null, "limit_remaining":75.0|null,
//	  "usage":25.0, "usage_daily":1.2, "usage_weekly":8.0, "usage_monthly":25.0,
//	  "is_free_tier":false,
//	  "free_model_daily_requests":{"used":0,"limit":50,"remaining":50},
//	  "budgets":[{"scope":"api_key","limit_usd":10,"spend_usd":3,
//	              "remaining_usd":7,"reset_interval":"weekly",
//	              "resets_at":"2026-11-16T00:00:00Z"}]
//	}}
type openRouterKeyResp struct {
	Data struct {
		Label           string   `json:"label"`
		Limit           *float64 `json:"limit"`
		LimitRemaining  *float64 `json:"limit_remaining"`
		Usage           float64  `json:"usage"`
		UsageDaily      float64  `json:"usage_daily"`
		UsageWeekly     float64  `json:"usage_weekly"`
		UsageMonthly    float64  `json:"usage_monthly"`
		IsFreeTier      bool     `json:"is_free_tier"`
		FreeModelDaily  *struct {
			Used      float64 `json:"used"`
			Limit     float64 `json:"limit"`
			Remaining float64 `json:"remaining"`
		} `json:"free_model_daily_requests"`
		Budgets []struct {
			Scope         string   `json:"scope"`
			GuardrailID   string   `json:"guardrail_id"`
			LimitUSD      *float64 `json:"limit_usd"`
			SpendUSD      float64  `json:"spend_usd"`
			RemainingUSD  *float64 `json:"remaining_usd"`
			ResetInterval string   `json:"reset_interval"`
			ResetsAt      string   `json:"resets_at"`
		} `json:"budgets"`
	} `json:"data"`
}

func parseOpenRouterKey(ctx context.Context, in Input) (Report, error) {
	base := strings.TrimRight(in.BaseURL, "/")
	// Accept both the chat base ("https://openrouter.ai/api/v1") and a
	// bare host; the key endpoint lives under /api/v1 either way.
	url := in.EndpointURL
	if url == "" {
		switch {
		case strings.HasSuffix(base, "/api/v1"):
			url = base + "/key"
		case strings.Contains(base, "openrouter.ai"):
			url = "https://openrouter.ai/api/v1/key"
		default:
			url = base + "/key"
		}
	}

	var resp openRouterKeyResp
	raw, err := getJSON(ctx, url, map[string]string{
		"Authorization": "Bearer " + in.APIKey,
	}, &resp)
	if err != nil {
		return Report{}, err
	}

	rep := Report{Raw: raw}
	d := resp.Data

	// Key-level limit. A null limit means "unlimited" — we still record
	// the usage number so the page can show spend without a bar.
	used := d.Usage
	lim := 0.0
	remaining := -1.0
	if d.Limit != nil {
		lim = *d.Limit
	}
	if d.LimitRemaining != nil {
		remaining = *d.LimitRemaining
	}
	if lim > 0 || used > 0 {
		rep.Points = append(rep.Points, UsagePoint{
			Window: WindowKeyLimit, Scope: "key",
			Used: used, Limit: lim, Remaining: remaining, Unit: UnitUSD,
			Label: d.Label,
		})
	}

	// Budgets: each has its own reset interval, so they become windows.
	// Several budgets may share an interval (a key limit plus a
	// workspace guardrail); keep them all, labelled.
	for _, b := range d.Budgets {
		p := UsagePoint{
			Window: normalizeResetInterval(b.ResetInterval),
			Scope:  b.Scope,
			Used:   b.SpendUSD,
			Unit:   UnitUSD,
		}
		if b.LimitUSD != nil {
			p.Limit = *b.LimitUSD
		}
		if b.RemainingUSD != nil {
			p.Remaining = *b.RemainingUSD
		}
		if p.Window == "" {
			// Unknown interval: still useful as a plain credit balance.
			p.Window = WindowCredits
		}
		if b.ResetsAt != "" {
			if t, err := time.Parse(time.RFC3339, b.ResetsAt); err == nil {
				p.ResetsAt = t.UTC()
			}
		}
		if b.GuardrailID != "" {
			p.Label = "guardrail " + b.GuardrailID
		}
		rep.Points = append(rep.Points, p)
	}

	// Usage-only windows when no budget carries the corresponding
	// interval: many keys have budgets but the account still reports
	// daily/weekly spend, and that is worth showing.
	hasWindow := func(name string) bool {
		for _, p := range rep.Points {
			if p.Window == name {
				return true
			}
		}
		return false
	}
	for _, uv := range []struct {
		name string
		used float64
	}{
		{WindowDaily, d.UsageDaily},
		{WindowWeekly, d.UsageWeekly},
		{WindowMonthly, d.UsageMonthly},
	} {
		if uv.used > 0 && !hasWindow(uv.name) {
			rep.Points = append(rep.Points, UsagePoint{
				Window: uv.name, Scope: "account",
				Used: uv.used, Remaining: -1, Unit: UnitUSD,
				Note: "spend only (no budget configured)",
			})
		}
	}

	// Free-tier request quota, if any.
	if d.FreeModelDaily != nil && d.FreeModelDaily.Limit > 0 {
		rep.Points = append(rep.Points, UsagePoint{
			Window: WindowDaily, Scope: "free_models",
			Label: "free model requests",
			Used:  d.FreeModelDaily.Used,
			// Remaining is meaningful but the unit differs from USD, so
			// keep the limit and let the UI label the unit.
			Limit: d.FreeModelDaily.Limit, Remaining: d.FreeModelDaily.Remaining,
			Unit: UnitRequests,
		})
	}

	if len(rep.Points) == 0 {
		rep.Warn = "OpenRouter returned no limits, budgets, or usage"
	}
	if d.IsFreeTier {
		rep.Warn = appendWarn(rep.Warn, "account is on the free tier")
	}
	return rep, nil
}

// normalizeResetInterval maps a vendor's reset cadence onto our window
// names. Unknown values return "" so the caller can fall back.
func normalizeResetInterval(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "daily", "day":
		return WindowDaily
	case "weekly", "week":
		return WindowWeekly
	case "monthly", "month":
		return WindowMonthly
	}
	return ""
}

func appendWarn(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}
