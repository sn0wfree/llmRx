package store

import "time"

// SQL implementation of UsageHistoryRepository + UsageStateRepository.
// dbStore embedding gives Postgres the same code via the dialect.

// AppendUsagePoint records one window observation. Dedup is the
// caller's policy (the usage monitor knows how often it may write).
func (s *dbStore) AppendUsagePoint(p UsagePoint) error {
	_, err := s.exec(
		`INSERT INTO channel_usage_points(channel_id, ts, source, win, used, limit_val, remaining, unit, resets_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ChannelID, toUnix(p.TS.UTC()), p.Source, p.Window,
		p.Used, p.LimitVal, p.Remaining, p.Unit, toUnix(p.ResetsAt))
	return err
}

// UsagePoints returns one channel's points since from, oldest first.
func (s *dbStore) UsagePoints(channelID int64, from time.Time, limit int) ([]UsagePoint, error) {
	query := `SELECT channel_id, ts, source, win, used, limit_val, remaining, unit, resets_at
	          FROM channel_usage_points WHERE channel_id = ? AND ts >= ? ORDER BY ts ASC`
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

	var out []UsagePoint
	for rows.Next() {
		var p UsagePoint
		var ts, resets int64
		if err := rows.Scan(&p.ChannelID, &ts, &p.Source, &p.Window,
			&p.Used, &p.LimitVal, &p.Remaining, &p.Unit, &resets); err != nil {
			return nil, err
		}
		p.TS = fromUnix(ts)
		if resets > 0 {
			p.ResetsAt = fromUnix(resets)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PruneUsagePoints deletes points older than cutoff.
func (s *dbStore) PruneUsagePoints(cutoff time.Time) (int64, error) {
	res, err := s.exec(`DELETE FROM channel_usage_points WHERE ts < ?`, toUnix(cutoff.UTC()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UpdateChannelUsageState stores the latest vendor usage report.
//
// Same contract as UpdateChannelBalance: on failure only the error and
// timestamp change, so the last good report stays visible (a stale
// quota is far more useful than a blank one). updated_at is untouched.
func (s *dbStore) UpdateChannelUsageState(channelID int64, state string, checkedAt time.Time, probeErr string) error {
	if len(probeErr) > 512 {
		probeErr = probeErr[:512]
	}
	if probeErr != "" {
		_, err := s.exec(
			`UPDATE channels SET usage_error = ?, usage_checked_at = ? WHERE id = ?`,
			probeErr, toUnix(checkedAt.UTC()), channelID)
		return err
	}
	_, err := s.exec(
		`UPDATE channels SET usage_state = ?, usage_error = '', usage_checked_at = ? WHERE id = ?`,
		state, toUnix(checkedAt.UTC()), channelID)
	return err
}
