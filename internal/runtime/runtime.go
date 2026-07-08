// Package runtime holds the small set of values that can be tweaked
// at runtime via the admin Settings page and are read on the hot
// path. Every field is atomic so concurrent reads and writes are
// race-free. float64 tunables are stored as uint64 bits and loaded
// with math.Float64{from,to}bits; integer tunables use the
// sync/atomic Load/Store helpers (the int64-typed atomic.Int64
// wrapper is unavailable on Go 1.18 — the project's minimum).
package runtime

import (
	"math"
	"sync/atomic"
)

// Defaults holds the current effective values. The admin Settings
// page reads from it via the API; the chat pipeline reads the
// per-request fields (MarkupRatio, CostStrategy) via a pointer to
// keep cache lines to a minimum.
type Defaults struct {
	markupBits uint64 // float64 bits via math.Float64{from,to}bits

	breakerMaxFailures    int64
	breakerResetTimeoutMs int64
	alertCooldownSec      int64
	logRetentionDays      int64

	costStrategy atomic.Value // string
}

// New returns Defaults seeded with sane defaults.
func New() *Defaults {
	d := &Defaults{}
	atomic.StoreInt64(&d.breakerMaxFailures, 5)
	atomic.StoreInt64(&d.breakerResetTimeoutMs, 30000)
	atomic.StoreInt64(&d.alertCooldownSec, 300)
	atomic.StoreInt64(&d.logRetentionDays, 30)
	d.SetMarkupRatio(1.0)
	d.SetCostStrategy("cheapest")
	return d
}

// ---- MarkupRatio (float64, atomic via uint64 bits) ----

// MarkupRatio is the current per-request billing multiplier. Reads
// and writes are atomic; the value is positive and defaults to 1.0
// (no markup).
func (d *Defaults) MarkupRatio() float64 {
	return math.Float64frombits(atomic.LoadUint64(&d.markupBits))
}

// SetMarkupRatio updates the multiplier; values <= 0 are ignored
// (1.0 is the floor).
func (d *Defaults) SetMarkupRatio(m float64) {
	if m <= 0 {
		m = 1.0
	}
	atomic.StoreUint64(&d.markupBits, math.Float64bits(m))
}

// ---- CostStrategy (string, atomic.Value) ----

// CostStrategy returns the active L3 router strategy.
func (d *Defaults) CostStrategy() string {
	v, _ := d.costStrategy.Load().(string)
	if v == "" {
		return "cheapest"
	}
	return v
}

// SetCostStrategy replaces the L3 strategy. Unknown values are kept
// as-is; the engine validates at use time.
func (d *Defaults) SetCostStrategy(s string) {
	if s == "" {
		s = "cheapest"
	}
	d.costStrategy.Store(s)
}

// ---- BreakerMaxFailures ----

// BreakerMaxFailures returns the per-channel failure threshold for
// the circuit breaker. Default 5.
func (d *Defaults) BreakerMaxFailures() int64 {
	return atomic.LoadInt64(&d.breakerMaxFailures)
}

// SetBreakerMaxFailures updates the threshold. Values <= 0 floor to
// the default of 5.
func (d *Defaults) SetBreakerMaxFailures(n int64) {
	if n <= 0 {
		n = 5
	}
	atomic.StoreInt64(&d.breakerMaxFailures, n)
}

// ---- BreakerResetTimeoutMs ----

// BreakerResetTimeoutMs returns the millisecond window after which
// a tripped channel is allowed half-open probes. Default 30000.
func (d *Defaults) BreakerResetTimeoutMs() int64 {
	return atomic.LoadInt64(&d.breakerResetTimeoutMs)
}

// SetBreakerResetTimeoutMs updates the reset window. Values < 100
// floor to 100; values above 24h are rejected.
func (d *Defaults) SetBreakerResetTimeoutMs(ms int64) {
	if ms < 100 {
		ms = 100
	}
	if ms > 24*60*60*1000 {
		ms = 24 * 60 * 60 * 1000
	}
	atomic.StoreInt64(&d.breakerResetTimeoutMs, ms)
}

// ---- AlertCooldownSec ----

// AlertCooldownSec returns the default cooldown applied to alert
// rules that don't specify their own. Default 300.
func (d *Defaults) AlertCooldownSec() int64 {
	return atomic.LoadInt64(&d.alertCooldownSec)
}

// SetAlertCooldownSec updates the default alert cooldown. Values
// < 0 floor to 0; values above 24h are rejected.
func (d *Defaults) SetAlertCooldownSec(sec int64) {
	if sec < 0 {
		sec = 0
	}
	if sec > 24*60*60 {
		sec = 24 * 60 * 60
	}
	atomic.StoreInt64(&d.alertCooldownSec, sec)
}

// ---- LogRetentionDays ----

// LogRetentionDays returns the log retention window in days. 0
// means "keep forever". Default 30.
func (d *Defaults) LogRetentionDays() int64 {
	return atomic.LoadInt64(&d.logRetentionDays)
}

// SetLogRetentionDays updates the retention window. Values < 0
// floor to 0; values above 10 years are rejected.
func (d *Defaults) SetLogRetentionDays(days int64) {
	if days < 0 {
		days = 0
	}
	if days > 3650 {
		days = 3650
	}
	atomic.StoreInt64(&d.logRetentionDays, days)
}
