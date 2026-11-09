// Package subusage asks the UPSTREAM VENDOR what our subscription
// quota looks like (P22).
//
// It is the vendor-side counterpart of internal/usage: that package
// counts requests llmRx itself served, while this one polls the
// provider's own API and reports what the provider believes was
// consumed. The two deliberately stay separate — they disagree (a
// channel can be shared with other tools) and the disagreement is
// information, not a bug.
//
// A report is a list of windows rather than one number because real
// plans have several at once: OpenRouter exposes a key limit plus
// daily/weekly/monthly budgets, and GLM's coding plan has a rolling
// 5-hour cap next to a weekly cap.
package subusage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Window names. Providers may add their own; these are the ones the UI
// knows how to label and order.
const (
	WindowKeyLimit = "key_limit"
	WindowDaily    = "daily"
	WindowWeekly   = "weekly"
	WindowMonthly  = "monthly"
	WindowRolling  = "rolling_5h"
	WindowCredits  = "credits"
)

// Units a window can be denominated in.
const (
	UnitUSD      = "usd"
	UnitTokens   = "tokens"
	UnitCredits  = "credits"
	UnitRequests = "requests"
)

// UsagePoint is one quota window reported by the vendor.
type UsagePoint struct {
	// Window is the reset cadence / limit name (WindowKeyLimit, …).
	Window string `json:"window"`
	// Scope is "account", "key" or "plan" — who the window belongs to.
	Scope string `json:"scope,omitempty"`
	// Label is an optional human override (e.g. a budget's own name).
	Label string `json:"label,omitempty"`

	Used float64 `json:"used"`
	// Limit is the cap; 0 means unlimited or unknown.
	Limit float64 `json:"limit,omitempty"`
	// Remaining is the vendor's own figure; -1 means "not reported".
	Remaining float64 `json:"remaining"`
	Unit      string  `json:"unit,omitempty"`

	// ResetsAt is when the window rolls over; zero means it does not
	// (a one-off credit balance, or a vendor that does not say).
	ResetsAt time.Time `json:"resets_at,omitempty"`
	Note     string    `json:"note,omitempty"`
}

// Ratio is the consumed fraction of the window, 0 when there is no
// usable limit. It prefers the vendor's remaining figure over
// used/limit because vendors that report both sometimes disagree
// (usage lagging behind limit_remaining).
func (p UsagePoint) Ratio() float64 {
	if p.Limit > 0 {
		if p.Remaining >= 0 {
			return clamp01((p.Limit - p.Remaining) / p.Limit)
		}
		return clamp01(p.Used / p.Limit)
	}
	return 0
}

// Exhausted reports whether the window has nothing left.
func (p UsagePoint) Exhausted() bool {
	if p.Limit <= 0 {
		return false
	}
	if p.Remaining >= 0 {
		return p.Remaining <= 0
	}
	return p.Used >= p.Limit
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Report is one probe's output.
type Report struct {
	Source    string       `json:"source"`
	FetchedAt time.Time    `json:"fetched_at"`
	Points    []UsagePoint `json:"points"`
	// Experimental marks scrapers whose endpoint is not a published
	// API (the UI shows a warning badge).
	Experimental bool `json:"experimental,omitempty"`
	// Warn carries a non-fatal issue (partial parse, vendor remark).
	Warn string `json:"warn,omitempty"`
	// Raw is the first bytes of the response, for operator debugging
	// when a vendor changes its shape.
	Raw string `json:"raw,omitempty"`

	channelID int64 // set by the monitor; not serialised
}

// Window returns the point for the given window name, or nil.
func (r Report) Window(name string) *UsagePoint {
	for i := range r.Points {
		if r.Points[i].Window == name {
			return &r.Points[i]
		}
	}
	return nil
}

// Tightest returns the window closest to exhaustion — the one that
// actually constrains traffic. Prefers windows that carry a limit.
func (r Report) Tightest() *UsagePoint {
	var best *UsagePoint
	bestRatio := -1.0
	for i := range r.Points {
		p := &r.Points[i]
		if p.Limit <= 0 {
			continue
		}
		ratio := p.Ratio()
		if ratio > bestRatio {
			bestRatio = ratio
			best = p
		}
	}
	return best
}

// Input is what a parser needs to run.
type Input struct {
	// APIKey is the channel's (decrypted) upstream key. Empty for
	// cookie-based scrapers.
	APIKey  string
	BaseURL string
	// Cookie is the decrypted session cookie (web scrapers only).
	Cookie string
	// EndpointURL overrides the parser's default endpoint.
	EndpointURL string
	// JSONPath / WindowName / Unit are used by the "custom" parser.
	JSONPath   string
	WindowName string
	Unit       string
}

// Parser fetches and interprets one vendor's usage response.
type Parser func(ctx context.Context, in Input) (Report, error)

var (
	parserMu sync.RWMutex
	parsers  = map[string]Parser{}
)

// RegisterParser installs a parser under name (lowercased). Later
// registrations replace earlier ones — the DB-backed config lets an
// operator override a built-in.
func RegisterParser(name string, p Parser) {
	parserMu.Lock()
	parsers[strings.ToLower(name)] = p
	parserMu.Unlock()
}

// LookupParser returns the parser registered under name.
func LookupParser(name string) (Parser, bool) {
	parserMu.RLock()
	p, ok := parsers[strings.ToLower(name)]
	parserMu.RUnlock()
	return p, ok
}

// ParserNames lists registered parsers, sorted for stable UI display.
func ParserNames() []string {
	parserMu.RLock()
	out := make([]string, 0, len(parsers))
	for k := range parsers {
		out = append(out, k)
	}
	parserMu.RUnlock()
	sortStrings(out)
	return out
}

// ErrUnsupported is returned when a channel's probe kind has no parser.
var ErrUnsupported = errors.New("subusage: probe kind unsupported")

// Probe runs the parser configured on the channel. It is the single
// entry point the monitor and the manual-refresh endpoint share.
func Probe(ctx context.Context, kind string, in Input) (Report, error) {
	if kind == "" {
		return Report{}, ErrUnsupported
	}
	p, ok := LookupParser(kind)
	if !ok {
		return Report{}, fmt.Errorf("%w: %s", ErrUnsupported, kind)
	}
	if in.EndpointURL == "" {
		// parsers own their defaults; nothing to do here
	}
	rep, err := p(ctx, in)
	if err != nil {
		return Report{}, err
	}
	if rep.FetchedAt.IsZero() {
		rep.FetchedAt = time.Now().UTC()
	}
	rep.Source = strings.ToLower(kind)
	return rep, nil
}

// sortStrings is a tiny insertion sort — the parser list is a handful
// of entries and this avoids pulling sort into the package's API
// surface for one call.
func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}
