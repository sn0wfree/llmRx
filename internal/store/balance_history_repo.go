package store

import "time"

// BalanceHistoryRepository persists the balance time series (P14 phase 3).
//
// Points live in their own table rather than on channels: one channel
// accumulates hundreds of them, and channels is read on the routing hot
// path, which should not carry history it never uses.
type BalanceHistoryRepository interface {
	// AppendBalancePoint records one observation. Callers are
	// responsible for the dedup window; this is a plain insert.
	AppendBalancePoint(channelID int64, at time.Time, balance, currency string) error

	// BalanceHistory returns one channel's points since `from`, oldest
	// first, capped at limit (0 = no cap, callers pass a sane bound).
	BalanceHistory(channelID int64, from time.Time, limit int) ([]BalancePoint, error)

	// LastBalancePoint returns the most recent point for a channel, or
	// ErrNotFound. Used to enforce the dedup window.
	LastBalancePoint(channelID int64) (*BalancePoint, error)

	// PruneBalanceHistory deletes points older than cutoff and returns
	// how many rows went away.
	PruneBalanceHistory(cutoff time.Time) (int64, error)
}

// BalancePoint is one observation of an upstream account balance.
//
// Balance stays a string to match channels.balance exactly, so a value
// read from the history and one read from the live snapshot are
// byte-identical and neither drifts through a float round-trip.
type BalancePoint struct {
	ChannelID int64     `json:"channel_id"`
	TS        time.Time `json:"ts"`
	Balance   string    `json:"balance"`
	Currency  string    `json:"currency"`
}
