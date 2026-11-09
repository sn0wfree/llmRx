package store

import (
	"time"

	"database/sql"
	"github.com/sn0wfree/llmRx/internal/dialect"
	"github.com/sn0wfree/llmRx/internal/model"
)

func (s *dbStore) GetPlans() ([]model.Plan, error) {
	rows, err := s.query(`SELECT id, name, budget_usd, used_usd, markup_ratio, status, created_at, updated_at FROM plans ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Plan
	for rows.Next() {
		var p model.Plan
		var status, created, updated int64
		if err := rows.Scan(&p.ID, &p.Name, &p.BudgetUSD, &p.UsedUSD, &p.MarkupRatio, &status, &created, &updated); err != nil {
			return nil, err
		}
		p.Status = int(status)
		p.CreatedAt = fromUnix(created)
		p.UpdatedAt = fromUnix(updated)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *dbStore) GetPlan(id int64) (*model.Plan, error) {
	row := s.queryRow(`SELECT id, name, budget_usd, used_usd, markup_ratio, status, created_at, updated_at FROM plans WHERE id = ?`, id)
	var p model.Plan
	var status, created, updated int64
	if err := row.Scan(&p.ID, &p.Name, &p.BudgetUSD, &p.UsedUSD, &p.MarkupRatio, &status, &created, &updated); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.Status = int(status)
	p.CreatedAt = fromUnix(created)
	p.UpdatedAt = fromUnix(updated)
	return &p, nil
}

func (s *dbStore) CreatePlan(p *model.Plan) error {
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	id, err := dialect.InsertOne(s.d, s.db,
		`INSERT INTO plans(name, budget_usd, used_usd, markup_ratio, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.BudgetUSD, p.UsedUSD, p.MarkupRatio, p.Status, toUnix(p.CreatedAt), toUnix(p.UpdatedAt),
	)
	if err != nil {
		return err
	}
	p.ID = id
	return nil
}

func (s *dbStore) UpdatePlan(p *model.Plan) error {
	p.UpdatedAt = time.Now().UTC()
	_, err := s.exec(
		`UPDATE plans SET name=?, budget_usd=?, used_usd=?, markup_ratio=?, status=?, updated_at=? WHERE id=?`,
		p.Name, p.BudgetUSD, p.UsedUSD, p.MarkupRatio, p.Status, toUnix(p.UpdatedAt), p.ID,
	)
	return err
}

// DeletePlan removes a plan row. Tokens that referenced it keep
// plan_id pointing at the now-missing row; the chat pipeline
// treats plan_id=0 (or unknown) as "no plan limit". Callers
// (admin handler) are expected to null out tokens.plan_id FIRST
// when an explicit unlink is desired.
func (s *dbStore) DeletePlan(id int64) error {
	_, err := s.exec(`DELETE FROM plans WHERE id=?`, id)
	return err
}

// ---------------- Users ----------------
