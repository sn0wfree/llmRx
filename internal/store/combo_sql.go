package store

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"database/sql"
	"github.com/sn0wfree/llmRx/internal/dialect"
	"github.com/sn0wfree/llmRx/internal/model"
)

func (s *dbStore) scanComboRow(r interface{ Scan(dest ...any) error }) (*model.TokenComboModel, error) {
	var c model.TokenComboModel
	var modelsJSON, mode, strategy, tiersJSON, fallbackJSON, ctxFallbackJSON string
	var enabled, isDefault any
	var created, updated int64
	if err := r.Scan(&c.ID, &c.TokenID, &c.Name, &modelsJSON, &mode, &strategy, &tiersJSON, &fallbackJSON, &ctxFallbackJSON, &enabled, &isDefault, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.Models = decodeStrings(modelsJSON)
	c.Mode = model.ComboMode(mode)
	c.Strategy = model.CostStrategy(strategy)
	c.Tiers = decodeTiers(tiersJSON)
	c.Fallback = decodeStrings(fallbackJSON)
	c.ContextFallback = decodeStrings(ctxFallbackJSON)
	c.Enabled = s.d.ParseBool(enabled)
	c.IsDefault = s.d.ParseBool(isDefault)
	c.CreatedAt = fromUnix(created)
	c.UpdatedAt = fromUnix(updated)
	return &c, nil
}

const comboCols = `id, token_id, name, models, mode, strategy, tiers, fallback, context_fallback, enabled, is_default, created_at, updated_at`

func (s *dbStore) GetComboModels(tokenID int64) ([]model.TokenComboModel, error) {
	rows, err := s.query(`SELECT `+comboCols+` FROM token_combo_models WHERE token_id = ? ORDER BY id`, tokenID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TokenComboModel
	for rows.Next() {
		c, err := s.scanComboRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *dbStore) GetComboModel(id int64) (*model.TokenComboModel, error) {
	row := s.queryRow(`SELECT `+comboCols+` FROM token_combo_models WHERE id = ?`, id)
	return s.scanComboRow(row)
}

func (s *dbStore) GetAllComboModels() ([]model.TokenComboModel, error) {
	rows, err := s.query(`SELECT `+comboCols+` FROM token_combo_models WHERE enabled = ? ORDER BY token_id, id`, s.d.Bool(true))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TokenComboModel
	for rows.Next() {
		c, err := s.scanComboRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ListAllComboModels returns every combo (enabled or disabled) across
// all tokens. Used by the admin UI's model-sets page where the
// operator wants to see disabled entries too. Routing-time lookup
// uses GetAllComboModels (enabled-only).
func (s *dbStore) ListAllComboModels() ([]model.TokenComboModel, error) {
	rows, err := s.query(`SELECT ` + comboCols + ` FROM token_combo_models ORDER BY token_id, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TokenComboModel
	for rows.Next() {
		c, err := s.scanComboRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *dbStore) CreateComboModel(c *model.TokenComboModel) error {
	if err := s.validateCombo(c); err != nil {
		return err
	}
	if c.IsDefault {
		if err := s.clearDefaultFlag(c.TokenID, 0); err != nil {
			return fmt.Errorf("clear default flag: %w", err)
		}
	}
	now := time.Now().Unix()
	comboID, err := dialect.InsertOne(s.d, s.db,
		`INSERT INTO token_combo_models (token_id, name, models, mode, strategy, tiers, fallback, context_fallback, enabled, is_default, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.TokenID, c.Name, encodeStrings(c.Models), string(c.Mode), string(c.Strategy), encodeTiers(c.Tiers), encodeStrings(c.Fallback), encodeStrings(c.ContextFallback), s.d.Bool(c.Enabled), s.d.Bool(c.IsDefault), now, now,
	)
	if err != nil {
		return err
	}
	c.ID = comboID
	c.CreatedAt = fromUnix(now)
	c.UpdatedAt = fromUnix(now)
	return nil
}

func (s *dbStore) validateCombo(c *model.TokenComboModel) error {
	// name format: ^[a-zA-Z0-9_-]{1,64}$
	if !comboNameRe.MatchString(c.Name) {
		return fmt.Errorf("combo name %q must match ^[a-zA-Z0-9_-]{1,64}$", c.Name)
	}
	// models: 1..100 items, each ^[a-zA-Z0-9._-]{1,128}$.
	// mode:auto combos may leave Models empty — the tier tables in
	// Tiers define the candidates instead.
	if c.Mode != model.ComboModeAuto {
		if len(c.Models) == 0 {
			return errors.New("combo models list must not be empty")
		}
		if len(c.Models) > 100 {
			return fmt.Errorf("combo models list has %d items, max is 100", len(c.Models))
		}
	}
	for _, m := range c.Models {
		if !comboModelRe.MatchString(m) {
			return fmt.Errorf("combo model name %q must match ^[a-zA-Z0-9._-]{1,128}$", m)
		}
	}
	// mode must be valid
	switch c.Mode {
	case model.ComboModeLoadBalance, model.ComboModeSerial:
		// ok
	case model.ComboModeAuto:
		if err := validateAutoCombo(c); err != nil {
			return err
		}
	default:
		return fmt.Errorf("combo mode %q must be load_balance, serial, or auto", c.Mode)
	}
	// strategy must be valid
	switch c.Strategy {
	case "", model.StrategyCheapest, model.StrategyFastest, model.StrategyBalanced, model.StrategyWeightedRandom:
		// ok
	default:
		return fmt.Errorf("combo strategy %q must be empty, cheapest, fastest, or balanced", c.Strategy)
	}
	// check name does not collide with any channel.Models real model name
	chs, err := s.GetChannels()
	if err != nil {
		return fmt.Errorf("check combo name conflict: %w", err)
	}
	for i := range chs {
		for _, m := range chs[i].Models {
			if m == c.Name {
				return fmt.Errorf("combo name %q conflicts with real model name in channel %q", c.Name, chs[i].Name)
			}
		}
	}
	return nil
}

var (
	comboNameRe  = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	comboModelRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,128}$`)
)

// validateAutoCombo checks the mode:auto specifics: every tier
// must be a known tier with a non-empty, syntactically valid
// model list; the fallback list (optional) must be syntactically
// valid.
func validateAutoCombo(c *model.TokenComboModel) error {
	if len(c.Tiers) == 0 {
		return errors.New("auto combo requires a non-empty tiers map")
	}
	if len(c.Tiers) > 16 {
		return fmt.Errorf("auto combo has %d tiers, max is 16", len(c.Tiers))
	}
	for tier, cfg := range c.Tiers {
		if !model.ValidAutoTier(tier) {
			return fmt.Errorf("auto combo tier %q is not a known tier (valid: %v)", tier, model.AutoTiers)
		}
		if len(cfg.Models) == 0 {
			return fmt.Errorf("auto combo tier %q models list must not be empty", tier)
		}
		if len(cfg.Models) > 100 {
			return fmt.Errorf("auto combo tier %q has %d models, max is 100", tier, len(cfg.Models))
		}
		for _, m := range cfg.Models {
			if !comboModelRe.MatchString(m) {
				return fmt.Errorf("auto combo tier %q model %q must match ^[a-zA-Z0-9._-]{1,128}$", tier, m)
			}
		}
	}
	for _, m := range c.Fallback {
		if !comboModelRe.MatchString(m) {
			return fmt.Errorf("auto combo fallback model %q must match ^[a-zA-Z0-9._-]{1,128}$", m)
		}
	}
	for _, m := range c.ContextFallback {
		if !comboModelRe.MatchString(m) {
			return fmt.Errorf("auto combo context_fallback model %q must match ^[a-zA-Z0-9._-]{1,128}$", m)
		}
	}
	return nil
}

func (s *dbStore) UpdateComboModel(c *model.TokenComboModel) error {
	if c.IsDefault {
		if err := s.clearDefaultFlag(c.TokenID, c.ID); err != nil {
			return fmt.Errorf("clear default flag: %w", err)
		}
	}
	now := time.Now().Unix()
	_, err := s.exec(
		`UPDATE token_combo_models SET name=?, models=?, mode=?, strategy=?, tiers=?, fallback=?, context_fallback=?, enabled=?, is_default=?, updated_at=? WHERE id=?`,
		c.Name, encodeStrings(c.Models), string(c.Mode), string(c.Strategy), encodeTiers(c.Tiers), encodeStrings(c.Fallback), encodeStrings(c.ContextFallback), s.d.Bool(c.Enabled), s.d.Bool(c.IsDefault), now, c.ID,
	)
	if err != nil {
		return err
	}
	c.UpdatedAt = fromUnix(now)
	return nil
}

func (s *dbStore) DeleteComboModel(id int64) error {
	_, err := s.exec(`DELETE FROM token_combo_models WHERE id = ?`, id)
	return err
}

// SetDefaultModelSet promotes one combo to the token's "default"
// set and demotes any other. The "auto" routing alias resolves to
// the default set. If comboID is 0 the call is a no-op (used when
// the operator wants to clear the default).
func (s *dbStore) SetDefaultModelSet(tokenID, comboID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(s.d.RewriteQuery(`UPDATE token_combo_models SET is_default = ?, updated_at = ? WHERE token_id = ? AND is_default = ?`),
		s.d.Bool(false), time.Now().Unix(), tokenID, s.d.Bool(true)); err != nil {
		return err
	}
	if comboID != 0 {
		if _, err := tx.Exec(s.d.RewriteQuery(`UPDATE token_combo_models SET is_default = ?, updated_at = ? WHERE id = ? AND token_id = ?`),
			s.d.Bool(true), time.Now().Unix(), comboID, tokenID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// clearDefaultFlag unsets is_default for every combo of tokenID
// except excludeID. Used inside Create/Update to keep the per-token
// default-set invariant at most one row has is_default=1. excludeID
// 0 means "no exclusion".
func (s *dbStore) clearDefaultFlag(tokenID, excludeID int64) error {
	if excludeID == 0 {
		_, err := s.exec(`UPDATE token_combo_models SET is_default = ?, updated_at = ? WHERE token_id = ?`, s.d.Bool(false),
			time.Now().Unix(), tokenID)
		return err
	}
	_, err := s.exec(`UPDATE token_combo_models SET is_default = ?, updated_at = ? WHERE token_id = ? AND id != ?`, s.d.Bool(false),
		time.Now().Unix(), tokenID, excludeID)
	return err
}

// ---------- Guardrails ----------
