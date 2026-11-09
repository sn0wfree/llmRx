package store

import (
	"time"

	"database/sql"
	"github.com/sn0wfree/llmRx/internal/dialect"
	"github.com/sn0wfree/llmRx/internal/model"
)

// channelColumns is the SELECT list for reading a channel. Kept in one
// place because scanChannel must stay in lockstep with it — a mismatch
// between the two is a runtime scan error, not a compile error.
const channelColumns = `id, name, provider, protocol, base_url, models, intents, priority, ` +
	`input_price, output_price, cached_input_discount, circuit_breaker, status, total_cost_usd, ` +
	`balance, balance_currency, balance_updated_at, balance_error, balance_url, balance_threshold, ` +
	`plan_quota, usage_probe, usage_state, usage_checked_at, usage_error, created_at, updated_at`

func (s *dbStore) GetChannels() ([]model.Channel, error) {
	rows, err := s.query(`SELECT ` + channelColumns + ` FROM channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Channel
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ch)
	}
	return out, rows.Err()
}

func (s *dbStore) GetChannel(id int64) (*model.Channel, error) {
	row := s.queryRow(`SELECT `+channelColumns+` FROM channels WHERE id = ?`, id)
	return scanChannel(row)
}

func (s *dbStore) CreateChannel(ch *model.Channel) error {
	now := time.Now().UTC()
	ch.CreatedAt = now
	ch.UpdatedAt = now
	if ch.Protocol == "" {
		ch.Protocol = "openai"
	}
	if ch.CachedInputDiscount == 0 {
		ch.CachedInputDiscount = 0.1
	}
	id, err := dialect.InsertOne(s.d, s.db,
		`INSERT INTO channels(name, provider, protocol, base_url, models, intents, priority, input_price, output_price, cached_input_discount, circuit_breaker, status, balance, balance_currency, balance_url, balance_threshold, plan_quota, usage_probe, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ch.Name, ch.Provider, ch.Protocol, ch.BaseURL,
		encodeStrings(ch.Models), encodeStrings(ch.Intents),
		ch.Priority, ch.InputPrice, ch.OutputPrice, ch.CachedInputDiscount, encodeCB(ch.CircuitBreaker),
		int(ch.Status),
		ch.Balance, ch.BalanceCurrency, ch.BalanceURL, ch.BalanceThreshold,
		encodePQ(ch.PlanQuota), encodeUP(ch.UsageProbe),
		toUnix(ch.CreatedAt), toUnix(ch.UpdatedAt),
	)
	if err != nil {
		return err
	}
	ch.ID = id
	return nil
}

func (s *dbStore) UpdateChannel(ch *model.Channel) error {
	ch.UpdatedAt = time.Now().UTC()
	if ch.Protocol == "" {
		ch.Protocol = "openai"
	}
	_, err := s.exec(
		`UPDATE channels SET name=?, provider=?, protocol=?, base_url=?, models=?, intents=?, priority=?, input_price=?, output_price=?, cached_input_discount=?, circuit_breaker=?, status=?, balance_url=?, balance_threshold=?, plan_quota=?, usage_probe=?, updated_at=? WHERE id=?`,
		ch.Name, ch.Provider, ch.Protocol, ch.BaseURL,
		encodeStrings(ch.Models), encodeStrings(ch.Intents),
		ch.Priority, ch.InputPrice, ch.OutputPrice, ch.CachedInputDiscount, encodeCB(ch.CircuitBreaker),
		int(ch.Status), ch.BalanceURL, ch.BalanceThreshold, encodePQ(ch.PlanQuota), encodeUP(ch.UsageProbe),
		toUnix(ch.UpdatedAt), ch.ID,
	)
	return err
}

func (s *dbStore) DeleteChannel(id int64) error {
	_, err := s.exec(`DELETE FROM channels WHERE id = ?`, id)
	return err
}

func (s *dbStore) RecordChannelSpend(channelID int64, cost float64) error {
	_, err := s.exec(`UPDATE channels SET total_cost_usd = total_cost_usd + ? WHERE id = ?`, cost, channelID)
	return err
}

// UpdateChannelBalance writes the latest upstream balance snapshot.
//
// Two distinct outcomes:
//   - success (balanceErr == ""): store the value and clear the error;
//   - failure: record the error and leave the last known balance intact,
//     so the UI can show "110 CNY (stale, last error: ...)" instead of
//     losing the only number the operator has.
//
// updated_at is intentionally NOT bumped: an hourly refresh would
// otherwise make every channel look freshly edited.
func (s *dbStore) UpdateChannelBalance(channelID int64, balance, currency string, checkedAt time.Time, balanceErr string) error {
	if len(balanceErr) > 512 {
		balanceErr = balanceErr[:512]
	}
	if balanceErr != "" {
		_, err := s.exec(
			`UPDATE channels SET balance_error = ?, balance_updated_at = ? WHERE id = ?`,
			balanceErr, toUnix(checkedAt), channelID)
		return err
	}
	_, err := s.exec(
		`UPDATE channels SET balance = ?, balance_currency = ?, balance_error = '', balance_updated_at = ? WHERE id = ?`,
		balance, currency, toUnix(checkedAt), channelID)
	return err
}

func (s *dbStore) GetDrainedChannels() ([]DrainedChannel, error) {
	rows, err := s.query(`SELECT c.id, c.name FROM channels c WHERE c.status = 1 AND NOT EXISTS (SELECT 1 FROM keys k WHERE k.channel_id = c.id AND k.status = 0)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DrainedChannel
	for rows.Next() {
		var d DrainedChannel
		if err := rows.Scan(&d.ID, &d.Name); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func scanChannel(r interface {
	Scan(dest ...any) error
}) (*model.Channel, error) {
	var ch model.Channel
	var modelsJSON, intentsJSON, cbJSON, pqJSON, upJSON string
	var status int
	var created, updated int64
	var balanceUpdated, usageChecked sql.NullInt64
	if err := r.Scan(&ch.ID, &ch.Name, &ch.Provider, &ch.Protocol, &ch.BaseURL,
		&modelsJSON, &intentsJSON, &ch.Priority,
		&ch.InputPrice, &ch.OutputPrice, &ch.CachedInputDiscount, &cbJSON, &status,
		&ch.TotalCostUSD,
		&ch.Balance, &ch.BalanceCurrency, &balanceUpdated, &ch.BalanceError, &ch.BalanceURL,
		&ch.BalanceThreshold,
		&pqJSON, &upJSON, &ch.UsageState, &usageChecked, &ch.UsageError,
		&created, &updated); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if balanceUpdated.Valid {
		t := fromUnix(balanceUpdated.Int64)
		ch.BalanceUpdatedAt = &t
	}
	if usageChecked.Valid {
		t := fromUnix(usageChecked.Int64)
		ch.UsageCheckedAt = &t
	}
	ch.Models = decodeStrings(modelsJSON)
	ch.Intents = decodeStrings(intentsJSON)
	if ch.Protocol == "" {
		ch.Protocol = "openai"
	}
	ch.CircuitBreaker = decodeCB(cbJSON)
	ch.PlanQuota = decodePQ(pqJSON)
	ch.UsageProbe = decodeUP(upJSON)
	ch.Status = model.ChannelStatus(status)
	ch.CreatedAt = fromUnix(created)
	ch.UpdatedAt = fromUnix(updated)
	return &ch, nil
}

// ---------------- Keys ----------------
