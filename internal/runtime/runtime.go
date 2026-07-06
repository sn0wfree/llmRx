// Package runtime holds the small set of values that can be tweaked
// at runtime via the admin Settings page and are read on the hot
// path. Each field is atomic so reads and writes are lock-free.
package runtime

import (
	"math"
	"sync/atomic"
)

// Defaults holds the current effective values. The admin Settings
// page reads from it via the API; the chat pipeline reads the
// per-request fields (MarkupRatio) via a pointer to keep cache lines
// to a minimum.
type Defaults struct {
	markupBits uint64 // float64 bits via math.Float64{from,to}bits

	BreakerMaxFailures    int64
	BreakerResetTimeoutMs int64
	AlertCooldownSec      int64
	LogRetentionDays      int64
	costStrategy          atomic.Value // string
}

// New returns Defaults seeded with sane defaults.
func New() *Defaults {
	d := &Defaults{
		BreakerMaxFailures:    5,
		BreakerResetTimeoutMs: 30000,
		AlertCooldownSec:      300,
		LogRetentionDays:      30,
	}
	d.SetMarkupRatio(1.0)
	d.SetCostStrategy("cheapest")
	return d
}

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
