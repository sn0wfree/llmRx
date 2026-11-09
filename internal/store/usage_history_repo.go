package store

import "time"

// UsageHistoryRepository persists the vendor-reported subscription
// usage time series (P22). One row per (channel, window) per probe, so
// the subscription page can plot "how fast am I burning the plan".
//
// Rows live in their own table rather than on channels for the same
// reason as balance history: channels is on the routing hot path.
type UsageHistoryRepository interface {
	// AppendUsagePoint records one window observation. Callers are
	// responsible for any dedup policy; this is a plain insert.
	AppendUsagePoint(p UsagePoint) error

	// UsagePoints returns one channel's points since `from`, oldest
	// first, capped at limit (0 = no cap).
	UsagePoints(channelID int64, from time.Time, limit int) ([]UsagePoint, error)

	// PruneUsagePoints deletes points older than cutoff and returns how
	// many rows went away.
	PruneUsagePoints(cutoff time.Time) (int64, error)
}

// UsageStateRepository persists the latest vendor report per channel.
type UsageStateRepository interface {
	// UpdateChannelUsageState stores the latest probe result. A
	// non-empty probeErr records a failure and leaves the previous
	// state intact; an empty one stores the state and clears the error.
	// Deliberately does not touch updated_at so a periodic probe does
	// not make every channel look freshly edited.
	UpdateChannelUsageState(channelID int64, state string, checkedAt time.Time, probeErr string) error
}

// UsagePoint is one vendor-reported quota window observation.
//
// Used is the consumed amount, LimitVal the cap (0 = unlimited/unknown),
// Remaining the vendor's own remaining figure (-1 = unknown). Keeping
// all three means the UI can show whatever the vendor exposes without
// inferring: some report only used, some only remaining.
type UsagePoint struct {
	ChannelID int64     `json:"channel_id"`
	TS        time.Time `json:"ts"`
	Source    string    `json:"source"` // parser name, e.g. "openrouter_key"
	Window    string    `json:"window"` // key_limit | daily | weekly | monthly | rolling_5h | credits
	Used      float64   `json:"used"`
	LimitVal  float64   `json:"limit"`
	Remaining float64   `json:"remaining"`
	Unit      string    `json:"unit"`      // usd | tokens | credits | requests
	ResetsAt  time.Time `json:"resets_at"` // zero = no reset
}
