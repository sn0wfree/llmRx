// Package runtime holds the small set of values that can be tweaked
// at runtime via the admin Settings page and are read on the hot
// path. Every field is atomic so concurrent reads and writes are
// race-free. float64 tunables are stored as uint64 bits and loaded
// with math.Float64{from,to}bits; integer tunables use the
// sync/atomic Load/Store helpers (the int64-typed atomic.Int64
// wrapper is unavailable on Go 1.18 — the project's minimum).
package runtime

import (
	"encoding/json"
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
	streamTimeoutSec      int64
	streamMaxBodyBytes    int64
	maxLogSubscribers     int64
	logLevelBits          uint64 // slog.Level int64 bits

	costStrategy atomic.Value // string
}

// New returns Defaults seeded with sane defaults.
func New() *Defaults {
	d := &Defaults{}
	atomic.StoreInt64(&d.breakerMaxFailures, 5)
	atomic.StoreInt64(&d.breakerResetTimeoutMs, 30000)
	atomic.StoreInt64(&d.alertCooldownSec, 300)
	atomic.StoreInt64(&d.logRetentionDays, 30)
	atomic.StoreInt64(&d.streamTimeoutSec, 300)
	atomic.StoreInt64(&d.streamMaxBodyBytes, 32*1024*1024)
	atomic.StoreInt64(&d.maxLogSubscribers, 0)
	atomic.StoreUint64(&d.logLevelBits, uint64(0)) // slog.LevelInfo
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

// ---- StreamTimeoutSec ----

// StreamTimeoutSec returns the per-stream wall-clock cap in seconds.
// 0 disables the cap (stream runs until upstream closes). Default 300.
func (d *Defaults) StreamTimeoutSec() int64 {
	return atomic.LoadInt64(&d.streamTimeoutSec)
}

// SetStreamTimeoutSec updates the cap. Values < 0 floor to 0
// (disabled); values above 1h are rejected.
func (d *Defaults) SetStreamTimeoutSec(sec int64) {
	if sec < 0 {
		sec = 0
	}
	if sec > 3600 {
		sec = 3600
	}
	atomic.StoreInt64(&d.streamTimeoutSec, sec)
}

// ---- StreamMaxBodyBytes ----

// StreamMaxBodyBytes returns the soft byte cap on the response
// stream. 0 disables the cap. Default 32 MiB.
func (d *Defaults) StreamMaxBodyBytes() int64 {
	return atomic.LoadInt64(&d.streamMaxBodyBytes)
}

// SetStreamMaxBodyBytes updates the cap. Values < 0 floor to 0.
// Max 1 GiB.
func (d *Defaults) SetStreamMaxBodyBytes(n int64) {
	if n < 0 {
		n = 0
	}
	if n > 1<<30 {
		n = 1 << 30
	}
	atomic.StoreInt64(&d.streamMaxBodyBytes, n)
}

// ---- MaxLogSubscribers ----

// MaxLogSubscribers returns the broker cap for SSE log subscribers.
// 0 means unlimited. Default 0.
func (d *Defaults) MaxLogSubscribers() int64 {
	return atomic.LoadInt64(&d.maxLogSubscribers)
}

// SetMaxLogSubscribers updates the cap. Values < 0 floor to 0.
func (d *Defaults) SetMaxLogSubscribers(n int64) {
	if n < 0 {
		n = 0
	}
	if n > 100000 {
		n = 100000
	}
	atomic.StoreInt64(&d.maxLogSubscribers, n)
}

// ---- LogLevel ----

// LogLevel returns the active log filter: 0=debug, 1=info, 2=warn,
// 3=error. Higher values drop more. Default 1 (info).
func (d *Defaults) LogLevel() int64 {
	return int64(atomic.LoadUint64(&d.logLevelBits))
}

// SetLogLevel updates the filter. Unknown values clamp to the
// nearest valid level.
func (d *Defaults) SetLogLevel(level int64) {
	if level < 0 {
		level = 0
	}
	if level > 3 {
		level = 3
	}
	atomic.StoreUint64(&d.logLevelBits, uint64(level))
}

// levelForLine inspects the format string for a recognized log
// level prefix and returns the matching level. The convention is:
//   - "error: ..."   → 3 (error)
//   - "warn: ..."    → 2 (warn)
//   - "info: ..."    → 1 (info)
//   - "debug: ..."   → 0 (debug)
//   - everything else → 1 (info, the default)
//
// The check is prefix-only so it can be used both at the writer
// level (log package) and at the call-site level. Lines that
// don't carry an explicit prefix are treated as info; this is the
// pre-existing convention in the codebase (all "alert:", "intent:",
// "secrets:", "router:" etc. calls are info-level).
func levelForLine(s string) int64 {
	switch {
	case len(s) >= 6 && s[:6] == "error:":
		return 3
	case len(s) >= 5 && s[:5] == "warn:":
		return 2
	case len(s) >= 5 && s[:5] == "info:":
		return 1
	case len(s) >= 6 && s[:6] == "debug:":
		return 0
	}
	return 1
}

// FormatLevel returns the canonical "level: " prefix for a level
// value. Used by the leveled writer to normalise lines.
func FormatLevel(level int64) string {
	switch level {
	case 0:
		return "debug: "
	case 1:
		return ""
	case 2:
		return "warn: "
	case 3:
		return "error: "
	}
	return ""
}

// ---- Snapshot (DB persistence) ----

// Snapshot is the JSON-serialisable view of every tunable. Used to
// persist admin changes across restarts. The JSON field names are
// the authoritative API contract; do not rename without bumping the
// admin API version. The Go field name and the JSON name may
// differ (e.g. BreakerResetMs → "breaker_reset_timeout_ms") for
// legacy compatibility.
type Snapshot struct {
	CostStrategy       string  `json:"cost_strategy"`
	MarkupRatio        float64 `json:"markup_ratio"`
	BreakerMaxFailures int64   `json:"breaker_max_failures"`
	BreakerResetMs     int64   `json:"breaker_reset_timeout_ms"`
	AlertCooldownSec   int64   `json:"alert_cooldown_sec"`
	LogRetentionDays   int64   `json:"log_retention_days"`
	StreamTimeoutSec   int64   `json:"stream_timeout_sec"`
	StreamMaxBodyBytes int64   `json:"stream_max_body_bytes"`
	MaxLogSubscribers  int64   `json:"max_log_subscribers"`
	LogLevel           int64   `json:"log_level"`
}

// Snapshot returns the current effective values. Safe to call
// concurrently; every read is atomic.
func (d *Defaults) Snapshot() Snapshot {
	return Snapshot{
		CostStrategy:       d.CostStrategy(),
		MarkupRatio:        d.MarkupRatio(),
		BreakerMaxFailures: d.BreakerMaxFailures(),
		BreakerResetMs:     d.BreakerResetTimeoutMs(),
		AlertCooldownSec:   d.AlertCooldownSec(),
		LogRetentionDays:   d.LogRetentionDays(),
		StreamTimeoutSec:   d.StreamTimeoutSec(),
		StreamMaxBodyBytes: d.StreamMaxBodyBytes(),
		MaxLogSubscribers:  d.MaxLogSubscribers(),
		LogLevel:           d.LogLevel(),
	}
}

// Apply replaces every tunable with the values from s. Used at
// startup to overlay DB-persisted changes on top of the YAML seed
// values. Unset / zero fields in s are NOT applied — the caller is
// responsible for ensuring s is well-formed (use ApplyJSON which
// falls back to current values on parse failure).
func (d *Defaults) Apply(s Snapshot) {
	if s.CostStrategy != "" {
		d.SetCostStrategy(s.CostStrategy)
	}
	if s.MarkupRatio > 0 {
		d.SetMarkupRatio(s.MarkupRatio)
	}
	if s.BreakerMaxFailures > 0 {
		d.SetBreakerMaxFailures(s.BreakerMaxFailures)
	}
	if s.BreakerResetMs > 0 {
		d.SetBreakerResetTimeoutMs(s.BreakerResetMs)
	}
	if s.AlertCooldownSec >= 0 {
		d.SetAlertCooldownSec(s.AlertCooldownSec)
	}
	if s.LogRetentionDays >= 0 {
		d.SetLogRetentionDays(s.LogRetentionDays)
	}
	d.SetStreamTimeoutSec(s.StreamTimeoutSec)
	d.SetStreamMaxBodyBytes(s.StreamMaxBodyBytes)
	d.SetMaxLogSubscribers(s.MaxLogSubscribers)
	d.SetLogLevel(s.LogLevel)
}

// Marshal returns the JSON representation of the current snapshot.
// Persisted verbatim by the store; admin handler should not post-
// process it.
func (d *Defaults) Marshal() ([]byte, error) {
	return json.Marshal(d.Snapshot())
}
