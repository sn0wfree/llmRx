package store

import (
	"errors"
	"time"

	"database/sql"
	"github.com/sn0wfree/llmRx/internal/dialect"
	"github.com/sn0wfree/llmRx/internal/model"
)

func (s *dbStore) scanGuardrailRow(r interface{ Scan(dest ...any) error }) (*model.GuardrailRule, error) {
	var g model.GuardrailRule
	var hook, onFailure, config string
	var enabled any
	var created, updated int64
	if err := r.Scan(&g.ID, &g.Name, &g.Description, &g.Type, &hook, &onFailure, &config, &g.Priority, &enabled, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	g.Hook = model.GuardrailHook(hook)
	g.OnFailure = model.GuardrailAction(onFailure)
	g.Config = config
	g.Enabled = s.d.ParseBool(enabled)
	g.CreatedAt = fromUnix(created)
	g.UpdatedAt = fromUnix(updated)
	return &g, nil
}

func (s *dbStore) GetEnabledGuardrailRules() ([]model.GuardrailRule, error) {
	rows, err := s.query(`SELECT id, name, description, type, hook, on_failure, config, priority, enabled, created_at, updated_at FROM guardrails WHERE enabled = ? ORDER BY priority, id`, s.d.Bool(true))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.GuardrailRule
	for rows.Next() {
		g, err := s.scanGuardrailRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

func (s *dbStore) GetGuardrailRules() ([]model.GuardrailRule, error) {
	rows, err := s.query(`SELECT id, name, description, type, hook, on_failure, config, priority, enabled, created_at, updated_at FROM guardrails ORDER BY priority, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.GuardrailRule
	for rows.Next() {
		g, err := s.scanGuardrailRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

func (s *dbStore) GetGuardrailRule(id int64) (*model.GuardrailRule, error) {
	row := s.queryRow(`SELECT id, name, description, type, hook, on_failure, config, priority, enabled, created_at, updated_at FROM guardrails WHERE id = ?`, id)
	return s.scanGuardrailRow(row)
}

func (s *dbStore) CreateGuardrailRule(r *model.GuardrailRule) error {
	now := time.Now().Unix()
	ruleID, err := dialect.InsertOne(s.d, s.db,
		`INSERT INTO guardrails (name, description, type, hook, on_failure, config, priority, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Name, r.Description, string(r.Type), string(r.Hook), string(r.OnFailure), r.Config, r.Priority, s.d.Bool(r.Enabled), now, now,
	)
	if err != nil {
		return err
	}
	r.ID = ruleID
	r.CreatedAt = fromUnix(now)
	r.UpdatedAt = fromUnix(now)
	return nil
}

func (s *dbStore) UpdateGuardrailRule(r *model.GuardrailRule) error {
	now := time.Now().Unix()
	_, err := s.exec(
		`UPDATE guardrails SET name=?, description=?, type=?, hook=?, on_failure=?, config=?, priority=?, enabled=?, updated_at=? WHERE id=?`,
		r.Name, r.Description, string(r.Type), string(r.Hook), string(r.OnFailure), r.Config, r.Priority, s.d.Bool(r.Enabled), now, r.ID,
	)
	if err != nil {
		return err
	}
	r.UpdatedAt = fromUnix(now)
	return nil
}

func (s *dbStore) DeleteGuardrailRule(id int64) error {
	_, err := s.exec(`DELETE FROM guardrails WHERE id = ?`, id)
	return err
}

func (s *dbStore) CreateGuardrailEvent(e *model.GuardrailEvent) error {
	now := time.Now().Unix()
	evID, err := dialect.InsertOne(s.d, s.db,
		`INSERT INTO guardrail_events (token_id, rule_id, rule_name, rule_type, hook, verdict, action, detail, request_ip, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.TokenID, e.RuleID, e.RuleName, e.RuleType, e.Hook, s.d.Bool(e.Verdict), e.Action, e.Detail, e.RequestIP, now,
	)
	if err != nil {
		return err
	}
	e.ID = evID
	e.CreatedAt = fromUnix(now)
	return nil
}

func (s *dbStore) GetGuardrailEvents(tokenID int64, limit int) ([]model.GuardrailEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.query(`SELECT id, token_id, rule_id, rule_name, rule_type, hook, verdict, action, detail, request_ip, created_at FROM guardrail_events WHERE token_id = ? ORDER BY created_at DESC LIMIT ?`, tokenID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.GuardrailEvent
	for rows.Next() {
		var e model.GuardrailEvent
		var verdict any
		var created int64
		if err := rows.Scan(&e.ID, &e.TokenID, &e.RuleID, &e.RuleName, &e.RuleType, &e.Hook, &verdict, &e.Action, &e.Detail, &e.RequestIP, &created); err != nil {
			return nil, err
		}
		e.Verdict = s.d.ParseBool(verdict)
		e.CreatedAt = fromUnix(created)
		out = append(out, e)
	}
	return out, rows.Err()
}
