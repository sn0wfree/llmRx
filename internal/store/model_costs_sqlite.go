package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

const modelCostCols = `id, model, provider,
	input_price_per_1m, output_price_per_1m,
	cache_read_price_per_1m, cache_write_price_per_1m,
	reasoning_price_per_1m, image_price,
	input_price_per_1m_above_128k, output_price_per_1m_above_128k,
	input_price_per_1m_above_200k, output_price_per_1m_above_200k,
	context_window, max_output, updated_at`

func scanModelCost(row scanner) (*model.ModelCost, error) {
	var c model.ModelCost
	var updated int64
	if err := row.Scan(&c.ID, &c.Model, &c.Provider,
		&c.InputPricePer1M, &c.OutputPricePer1M,
		&c.CacheReadPricePer1M, &c.CacheWritePricePer1M,
		&c.ReasoningPricePer1M, &c.ImagePrice,
		&c.InputPricePer1MAbove128K, &c.OutputPricePer1MAbove128K,
		&c.InputPricePer1MAbove200K, &c.OutputPricePer1MAbove200K,
		&c.ContextWindow, &c.MaxOutput, &updated); err != nil {
		return nil, err
	}
	c.UpdatedAt = fromUnix(updated)
	return &c, nil
}

func (s *dbStore) GetModelCost(model string) (*model.ModelCost, error) {
	row := s.queryRow(`SELECT `+modelCostCols+` FROM model_costs WHERE model = ?`, model)
	c, err := scanModelCost(row)
	if err != nil {
		if errors.Is(err, errNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return c, nil
}

func (s *dbStore) ListModelCosts() ([]model.ModelCost, error) {
	rows, err := s.query(`SELECT ` + modelCostCols + ` FROM model_costs ORDER BY model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ModelCost
	for rows.Next() {
		c, err := scanModelCost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *dbStore) UpsertModelCost(c *model.ModelCost) error {
	c.UpdatedAt = time.Now().UTC()
	_, err := s.exec(`
		INSERT INTO model_costs(model, provider,
			input_price_per_1m, output_price_per_1m,
			cache_read_price_per_1m, cache_write_price_per_1m,
			reasoning_price_per_1m, image_price,
			input_price_per_1m_above_128k, output_price_per_1m_above_128k,
			input_price_per_1m_above_200k, output_price_per_1m_above_200k,
			context_window, max_output, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(model) DO UPDATE SET
			provider = excluded.provider,
			input_price_per_1m = excluded.input_price_per_1m,
			output_price_per_1m = excluded.output_price_per_1m,
			cache_read_price_per_1m = excluded.cache_read_price_per_1m,
			cache_write_price_per_1m = excluded.cache_write_price_per_1m,
			reasoning_price_per_1m = excluded.reasoning_price_per_1m,
			image_price = excluded.image_price,
			input_price_per_1m_above_128k = excluded.input_price_per_1m_above_128k,
			output_price_per_1m_above_128k = excluded.output_price_per_1m_above_128k,
			input_price_per_1m_above_200k = excluded.input_price_per_1m_above_200k,
			output_price_per_1m_above_200k = excluded.output_price_per_1m_above_200k,
			context_window = excluded.context_window,
			max_output = excluded.max_output,
			updated_at = excluded.updated_at`,
		c.Model, c.Provider,
		c.InputPricePer1M, c.OutputPricePer1M,
		c.CacheReadPricePer1M, c.CacheWritePricePer1M,
		c.ReasoningPricePer1M, c.ImagePrice,
		c.InputPricePer1MAbove128K, c.OutputPricePer1MAbove128K,
		c.InputPricePer1MAbove200K, c.OutputPricePer1MAbove200K,
		c.ContextWindow, c.MaxOutput, toUnix(c.UpdatedAt),
	)
	return err
}

func (s *dbStore) DeleteModelCost(model string) error {
	_, err := s.exec(`DELETE FROM model_costs WHERE model = ?`, model)
	return err
}

func (s *dbStore) BatchUpsertModelCosts(costs []model.ModelCost) error {
	if len(costs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	for i := range costs {
		c := &costs[i]
		c.UpdatedAt = now
		_, err := tx.Exec(s.d.RewriteQuery(`
			INSERT INTO model_costs(model, provider,
				input_price_per_1m, output_price_per_1m,
				cache_read_price_per_1m, cache_write_price_per_1m,
				reasoning_price_per_1m, image_price,
				input_price_per_1m_above_128k, output_price_per_1m_above_128k,
				input_price_per_1m_above_200k, output_price_per_1m_above_200k,
				context_window, max_output, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(model) DO UPDATE SET
				provider = excluded.provider,
				input_price_per_1m = excluded.input_price_per_1m,
				output_price_per_1m = excluded.output_price_per_1m,
				cache_read_price_per_1m = excluded.cache_read_price_per_1m,
				cache_write_price_per_1m = excluded.cache_write_price_per_1m,
				reasoning_price_per_1m = excluded.reasoning_price_per_1m,
				image_price = excluded.image_price,
				input_price_per_1m_above_128k = excluded.input_price_per_1m_above_128k,
				output_price_per_1m_above_128k = excluded.output_price_per_1m_above_128k,
				input_price_per_1m_above_200k = excluded.input_price_per_1m_above_200k,
				output_price_per_1m_above_200k = excluded.output_price_per_1m_above_200k,
				context_window = excluded.context_window,
				max_output = excluded.max_output,
				updated_at = excluded.updated_at`),
			c.Model, c.Provider,
			c.InputPricePer1M, c.OutputPricePer1M,
			c.CacheReadPricePer1M, c.CacheWritePricePer1M,
			c.ReasoningPricePer1M, c.ImagePrice,
			c.InputPricePer1MAbove128K, c.OutputPricePer1MAbove128K,
			c.InputPricePer1MAbove200K, c.OutputPricePer1MAbove200K,
			c.ContextWindow, c.MaxOutput, toUnix(c.UpdatedAt),
		)
		if err != nil {
			return fmt.Errorf("upsert model cost %s: %w", c.Model, err)
		}
	}
	return tx.Commit()
}

func (s *dbStore) CountModelCosts() (int, error) {
	var n int
	if err := s.queryRow(`SELECT COUNT(*) FROM model_costs`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// BackfillTokenUserIDs links tokens to their owning user via
// token.name == users.username (the registration contract). Returns
// the number of tokens updated. Idempotent: only fills user_id = 0.
func (s *dbStore) BackfillTokenUserIDs(ctx context.Context) (int64, error) {
	res, err := s.execContext(ctx, `
		UPDATE tokens SET user_id = (
			SELECT id FROM users WHERE users.username = tokens.name LIMIT 1
		)
		WHERE user_id = 0 AND name != '' AND EXISTS (
			SELECT 1 FROM users WHERE users.username = tokens.name
		)`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
