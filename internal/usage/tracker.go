// Package usage tracks per-channel consumption against the upstream
// subscription plan quota each channel declares (P21).
//
// The tracker is pure in-memory state fed by the request emitter; the
// JSONL usage logs remain the source of truth. On restart the gateway
// replays recent rows through Recover, so no extra persistence is
// needed. Two kinds of windows are maintained per channel:
//
//   - calendar windows (daily / monthly UTC): a period key plus a
//     cumulative counter that resets when the period flips;
//   - rolling windows (trailing N hours): 5-minute buckets kept for
//     up to rollingRetention, summed on demand.
//
// Observe writes both calendar kinds and the rolling ring for every
// channel; Usage(ch, now) interprets only the window/metric the
// channel's PlanQuota actually declares. Observe is O(1) with a
// single mutex — the emitter calls it once per completed request.
package usage

import (
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/subusage"
)

const (
	// bucketSpan is the granularity of the rolling ring. 5 minutes
	// keeps the window sum accurate to within one bucket of the true
	// trailing window while bounding ring size.
	bucketSpan = 5 * time.Minute
	// rollingRetention bounds how long rolling buckets are retained.
	// Coding-plan rolling windows (GLM/Claude style) are a few hours;
	// 48h covers anything realistic. A channel configured with a
	// longer rolling window simply sees the retained tail.
	rollingRetention = 48 * time.Hour
)

// WindowUsage is one channel's consumption inside its quota window.
type WindowUsage struct {
	// WindowStart/WindowEnd bound [start, end) of the window that
	// contains Now. For rolling windows End == the query time.
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	// ResetAt is when the window rolls over (== WindowEnd for
	// calendar windows; zero for rolling windows, which slide).
	ResetAt time.Time `json:"reset_at"`

	Requests  int64   `json:"requests"`
	Prompt    int64   `json:"prompt_tokens"`
	Completion int64  `json:"completion_tokens"`
	Cached    int64   `json:"cached_tokens"`
	Reasoning int64   `json:"reasoning_tokens"`
	RealUSD   float64 `json:"real_cost_usd"`

	// Used/Limit are the quota quantities interpreted per
	// PlanQuota.Metric; Ratio = Used/Limit (0 when untracked).
	Used  float64 `json:"used"`
	Limit float64 `json:"limit"`
	Ratio float64 `json:"ratio"`
}

// counters is the raw consumption accumulated in one window or bucket.
type counters struct {
	Requests   int64
	Prompt     int64
	Completion int64
	Cached     int64
	Reasoning  int64
	RealUSD    float64
}

func (c *counters) add(prompt, completion, cached, reasoning int, realUSD float64) {
	c.Requests++
	c.Prompt += int64(prompt)
	c.Completion += int64(completion)
	c.Cached += int64(cached)
	c.Reasoning += int64(reasoning)
	c.RealUSD += realUSD
}

func (c *counters) plus(o counters) {
	c.Requests += o.Requests
	c.Prompt += o.Prompt
	c.Completion += o.Completion
	c.Cached += o.Cached
	c.Reasoning += o.Reasoning
	c.RealUSD += o.RealUSD
}

// calendar tracks the daily and monthly cumulative counters of one
// channel. Each window kind resets when the period key flips.
type calendar struct {
	daily   counters
	dayKey  string // "2006-01-02" UTC
	monthly counters
	monKey  string // "2006-01" UTC
}

// Tracker holds per-channel usage windows.
type Tracker struct {
	mu sync.Mutex
	// cal maps channelID → its calendar windows. Both daily and
	// monthly counters are always maintained; Usage picks the kind
	// the channel's PlanQuota declares.
	cal map[int64]*calendar
	// roll maps channelID → rolling buckets keyed by bucket start
	// (unix seconds, aligned to bucketSpan).
	roll map[int64]map[int64]*counters
}

// New returns an empty tracker.
func New() *Tracker {
	return &Tracker{
		cal:  map[int64]*calendar{},
		roll: map[int64]map[int64]*counters{},
	}
}

// Observe folds one completed request into the channel's windows.
// Safe for concurrent use; the router reads while the emitter writes.
func (t *Tracker) Observe(channelID int64, prompt, completion, cached, reasoning int, realUSD float64, at time.Time) {
	if channelID <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	c := t.cal[channelID]
	if c == nil {
		c = &calendar{}
		t.cal[channelID] = c
	}
	day := at.UTC().Format("2006-01-02")
	mon := at.UTC().Format("2006-01")
	if c.dayKey != day {
		c.dayKey = day
		c.daily = counters{}
	}
	if c.monKey != mon {
		c.monKey = mon
		c.monthly = counters{}
	}
	c.daily.add(prompt, completion, cached, reasoning, realUSD)
	c.monthly.add(prompt, completion, cached, reasoning, realUSD)

	r := t.roll[channelID]
	if r == nil {
		r = map[int64]*counters{}
		t.roll[channelID] = r
	}
	b := at.UTC().Unix() - at.UTC().Unix()%int64(bucketSpan/time.Second)
	bc := r[b]
	if bc == nil {
		bc = &counters{}
		r[b] = bc
		// Prune buckets past the retention tail so rings cannot grow
		// without bound; amortised over appends.
		cutoff := b - int64(rollingRetention/time.Second)
		for start := range r {
			if start < cutoff {
				delete(r, start)
			}
		}
	}
	bc.add(prompt, completion, cached, reasoning, realUSD)
}

// WindowRange returns [start, end) of the quota window that contains
// now for the given quota config. For rolling windows end == now and
// start slides. Returns zero times for untracked or unknown windows.
func WindowRange(q model.PlanQuota, now time.Time) (time.Time, time.Time) {
	n := now.UTC()
	switch q.Window {
	case model.QuotaWindowMonthly:
		start := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0)
	case model.QuotaWindowDaily:
		start := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 0, 1)
	case model.QuotaWindowRolling:
		if q.WindowHours <= 0 {
			return time.Time{}, time.Time{}
		}
		return n.Add(-time.Duration(q.WindowHours) * time.Hour), n
	default:
		return time.Time{}, time.Time{}
	}
}

// Usage reports the channel's consumption in the window its PlanQuota
// declares. Untracked channels (Limit == 0) return Ratio 0 with the
// calendar counters zeroed — the caller can skip them cheaply.
func (t *Tracker) Usage(ch *model.Channel, now time.Time) WindowUsage {
	q := ch.PlanQuota
	if !q.Tracked() {
		return WindowUsage{}
	}
	start, end := WindowRange(q, now)
	if start.IsZero() {
		return WindowUsage{}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	var c counters
	switch q.Window {
	case model.QuotaWindowMonthly:
		if cal := t.cal[ch.ID]; cal != nil && cal.monKey == now.UTC().Format("2006-01") {
			// Only the counters of the *current* period count; a
			// channel last observed last month carries stale keys.
			c = cal.monthly
		}
	case model.QuotaWindowDaily:
		if cal := t.cal[ch.ID]; cal != nil && cal.dayKey == now.UTC().Format("2006-01-02") {
			c = cal.daily
		}
	case model.QuotaWindowRolling:
		cutoff := start.UTC().Unix() - start.UTC().Unix()%int64(bucketSpan/time.Second)
		for bstart, bc := range t.roll[ch.ID] {
			if bstart >= cutoff {
				c.plus(*bc)
			}
		}
	}

	u := WindowUsage{
		WindowStart: start,
		WindowEnd:   end,
		Requests:    c.Requests,
		Prompt:      c.Prompt,
		Completion:  c.Completion,
		Cached:      c.Cached,
		Reasoning:   c.Reasoning,
		RealUSD:     c.RealUSD,
		Limit:       q.Limit,
	}
	if q.Window != model.QuotaWindowRolling {
		u.ResetAt = end
	}
	switch q.Metric {
	case model.QuotaMetricPromptTokens:
		u.Used = float64(c.Prompt)
	case model.QuotaMetricUSD:
		u.Used = c.RealUSD
	default: // model.QuotaMetricTokens: prompt + completion (reasoning is a subset of completion)
		u.Used = float64(c.Prompt + c.Completion)
	}
	if u.Limit > 0 {
		u.Ratio = u.Used / u.Limit
	}
	return u
}

// UsageRatio is the routing-path fast path: the consumed fraction of
// the channel's quota window (0 when untracked). Channels past 1.0
// are quota-exhausted.
func (t *Tracker) UsageRatio(ch *model.Channel, now time.Time) float64 {
	if !ch.PlanQuota.Tracked() {
		return 0
	}
	return t.Usage(ch, now).Ratio
}

// SourceRatio returns the vendor-reported consumed fraction (P22) for
// the channel's most constrained live window, so the router honours
// quota burned by other tools sharing the same upstream account.
//
// It returns ok=false — meaning "fall back to the local ratio" — when
// there is no probe, no cached report, or every window the vendor
// reported has already reset. A stale report is worse than none: it
// would keep a channel sidelined after its window rolled over.
func (t *Tracker) SourceRatio(ch *model.Channel, now time.Time) (float64, bool) {
	if !ch.UsageProbe.Enabled() || ch.UsageState == "" {
		return 0, false
	}
	rep := subusage.ReportFromState(ch.UsageState)
	if rep.Source == "" || len(rep.Points) == 0 {
		return 0, false
	}

	tightest := -1.0
	live := false
	for i := range rep.Points {
		p := rep.Points[i]
		// Skip windows the vendor says have rolled over.
		if !p.ResetsAt.IsZero() && !p.ResetsAt.After(now) {
			continue
		}
		live = true
		if p.Limit <= 0 {
			continue
		}
		r := p.Ratio()
		if r > tightest {
			tightest = r
		}
	}
	if !live {
		return 0, false
	}
	if tightest < 0 {
		// Vendor reports usage but no limits at all: nothing to enforce.
		return 0, false
	}
	return tightest, true
}

// Snapshot returns Usage for every channel, keyed by channel ID.
// Convenience for the monitoring page.
func (t *Tracker) Snapshot(channels []*model.Channel, now time.Time) map[int64]WindowUsage {
	out := make(map[int64]WindowUsage, len(channels))
	for _, ch := range channels {
		out[ch.ID] = t.Usage(ch, now)
	}
	return out
}
