package store

import (
	"time"
)

// SQL implementation of BalanceHistoryRepository. dbStore embedding
// gives Postgres the same code via the dialect.

// AppendBalancePoint records one observation.
//
// Deliberately not guarded against duplicates here: the dedup window is
// a policy decision that belongs to the caller (the balance monitor),
// which knows how often it is allowed to write.
func (s *dbStore) AppendBalancePoint(channelID int64, at time.Time, balance, currency string) error {
	_, err := s.exec(
		`INSERT INTO channel_balances(channel_id, ts, balance, currency) VALUES (?, ?, ?, ?)`,
		channelID, toUnix(at.UTC()), balance, currency)
	return err
}

// BalanceHistory returns one channel's points since from, oldest first.
func (s *dbStore) BalanceHistory(channelID int64, from time.Time, limit int) ([]BalancePoint, error) {
	query := `SELECT channel_id, ts, balance, currency FROM channel_balances
	          WHERE channel_id = ? AND ts >= ? ORDER BY ts ASC`
	args := []any{channelID, toUnix(from.UTC())}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BalancePoint
	for rows.Next() {
		var p BalancePoint
		var ts int64
		if err := rows.Scan(&p.ChannelID, &ts, &p.Balance, &p.Currency); err != nil {
			return nil, err
		}
		p.TS = fromUnix(ts)
		out = append(out, p)
	}
	return out, rows.Err()
}

// LastBalancePoint returns the newest point for a channel.
func (s *dbStore) LastBalancePoint(channelID int64) (*BalancePoint, error) {
	var p BalancePoint
	var ts int64
	err := s.queryRow(
		`SELECT channel_id, ts, balance, currency FROM channel_balances
		 WHERE channel_id = ? ORDER BY ts DESC LIMIT 1`, channelID).
		Scan(&p.ChannelID, &ts, &p.Balance, &p.Currency)
	if err != nil {
		return nil, ErrNotFound
	}
	p.TS = fromUnix(ts)
	return &p, nil
}

// PruneBalanceHistory deletes points older than cutoff.
//
// Any error is returned so the caller can log it, but a failure to prune
// must never fail a sweep: retention falling behind is a disk-space
// problem, not a correctness one.
func (s *dbStore) PruneBalanceHistory(cutoff time.Time) (int64, error) {
	res, err := s.exec(`DELETE FROM channel_balances WHERE ts < ?`, toUnix(cutoff.UTC()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UpdateKeyBalance stores one key's balance snapshot (P14 phase 4).
//
// Same contract as UpdateChannelBalance: a failure records the error and
// leaves the last known value, because a stale number is more useful to
// an operator than none.
func (s *dbStore) UpdateKeyBalance(keyID int64, balance, currency string, checkedAt time.Time, balanceErr string) error {
	if len(balanceErr) > 512 {
		balanceErr = balanceErr[:512]
	}
	if balanceErr != "" {
		_, err := s.exec(
			`UPDATE keys SET balance_error = ?, balance_updated_at = ? WHERE id = ?`,
			balanceErr, toUnix(checkedAt.UTC()), keyID)
		return err
	}
	_, err := s.exec(
		`UPDATE keys SET balance = ?, balance_currency = ?, balance_error = '', balance_updated_at = ? WHERE id = ?`,
		balance, currency, toUnix(checkedAt.UTC()), keyID)
	return err
}

// ClearKeyBalance resets a key's balance columns.
func (s *dbStore) ClearKeyBalance(keyID int64) error {
	_, err := s.exec(
		`UPDATE keys SET balance = '', balance_currency = '', balance_updated_at = NULL, balance_error = '' WHERE id = ?`,
		keyID)
	return err
}
