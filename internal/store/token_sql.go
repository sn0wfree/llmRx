package store

import (
	"errors"
	"fmt"
	"time"

	"database/sql"
	"github.com/sn0wfree/llmRx/internal/dialect"
	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/model"
)

func (s *dbStore) GetToken(key string) (*model.Token, error) {
	// With ciphertext-only mode, the SQL `key = ?` lookup can't
	// match an encrypted bearer directly. Fall back to scanning
	// all rows and comparing in plaintext — production callers
	// go through tokencache (in-memory), so this path is for
	// tests / recovery tools only.
	rows, err := s.query(`SELECT id, plan_id, key, key_ciphertext, name, status, rpm, tpm, used_usd, models_whitelist, ip_whitelist, expires_at, last_used_at, created_at, user_id FROM tokens`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		t, err := scanTokenRow(s, rows)
		if err != nil {
			return nil, err
		}
		if t.Key == key {
			return t, nil
		}
	}
	return nil, ErrNotFound
}

func (s *dbStore) GetTokenByID(id int64) (*model.Token, error) {
	row := s.queryRow(`SELECT id, plan_id, key, key_ciphertext, name, status, rpm, tpm, used_usd, models_whitelist, ip_whitelist, expires_at, last_used_at, created_at, user_id FROM tokens WHERE id = ?`, id)
	return scanTokenRow(s, row)
}

// GetTokenByName returns the token whose name matches. Primarily
// used to resolve the dedicated token created at registration
// (name == username). Name is not unique at the schema level, so
// prefer the registration-flow contract (one token per user name).
func (s *dbStore) GetTokenByName(name string) (*model.Token, error) {
	row := s.queryRow(`SELECT id, plan_id, key, key_ciphertext, name, status, rpm, tpm, used_usd, models_whitelist, ip_whitelist, expires_at, last_used_at, created_at, user_id FROM tokens WHERE name = ? ORDER BY id LIMIT 1`, name)
	return scanTokenRow(s, row)
}

// GetTokensByUserID returns all tokens owned by a user (user_id
// linkage, backfilled at startup). Empty result when the user has no
// tokens (admin-created users).
func (s *dbStore) GetTokensByUserID(userID int64) ([]model.Token, error) {
	rows, err := s.query(`SELECT id, plan_id, key, key_ciphertext, name, status, rpm, tpm, used_usd, models_whitelist, ip_whitelist, expires_at, last_used_at, created_at, user_id FROM tokens WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Token
	for rows.Next() {
		t, err := scanTokenRow(s, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *dbStore) GetTokens() ([]model.Token, error) {
	rows, err := s.query(`SELECT id, plan_id, key, key_ciphertext, name, status, rpm, tpm, used_usd, models_whitelist, ip_whitelist, expires_at, last_used_at, created_at, user_id FROM tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Token
	for rows.Next() {
		t, err := scanTokenRow(s, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *dbStore) CreateToken(t *model.Token) error {
	t.CreatedAt = time.Now().UTC()
	plain := t.Key
	if plain == "" {
		return errors.New("token key is empty")
	}
	cipher := ""
	storedPlain := plain
	if s.secrets != nil {
		ct, err := s.secrets.Encrypt([]byte(plain))
		if err != nil {
			return fmt.Errorf("encrypt token: %w", err)
		}
		cipher = ct
		// key column has a UNIQUE constraint but in encrypted mode
		// every row would store the empty string, violating it after
		// the second insert. Insert a sentinel placeholder now and
		// swap it for the row id once SQLite has generated one —
		// the placeholder is unique within the table so the UNIQUE
		// check passes, and the post-insert UPDATE uses the same id
		// to produce a stable, collision-free value (e.g.
		// "__enc_3" for token id 3). Legacy plaintext rows are
		// untouched and continue to dedup on the real key.
		storedPlain = "__enc_pending__"
	}
	id, err := dialect.InsertOne(s.d, s.db,
		`INSERT INTO tokens(plan_id, key, key_ciphertext, name, status, rpm, tpm, used_usd, models_whitelist, ip_whitelist, expires_at, last_used_at, created_at, user_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.PlanID, storedPlain, cipher, t.Name, int(t.Status), t.RPM, t.TPM, t.UsedUSD,
		encodeStrings(t.ModelsWhitelist), encodeStrings(t.IPWhitelist),
		toUnix(t.ExpiresAt), toUnix(t.LastUsedAt), toUnix(t.CreatedAt), t.UserID,
	)
	if err != nil {
		return err
	}
	t.ID = id
	if s.secrets != nil {
		// Replace the sentinel with "__enc_<id>" so it stays unique
		// and gives a deterministic lookup if anyone wants to identify
		// the row from its key column. scanTokenRow never reads the
		// key column when ciphertext is present, so this is purely a
		// UNIQUE-constraint workaround.
		_, _ = s.exec(`UPDATE tokens SET key=? WHERE id=?`, fmt.Sprintf("__enc_%d", id), id)
	}
	return nil
}

func (s *dbStore) UpdateToken(t *model.Token) error {
	t.LastUsedAt = time.Now().UTC()
	plain := t.Key
	cipher := ""
	storedPlain := plain
	if plain != "" && s.secrets != nil {
		ct, err := s.secrets.Encrypt([]byte(plain))
		if err != nil {
			return fmt.Errorf("encrypt token: %w", err)
		}
		cipher = ct
		// Same UNIQUE-constraint workaround as CreateToken: store
		// a stable placeholder derived from the row id (which is
		// already known for an UPDATE) rather than the empty
		// string. Plaintext-mode updates leave storedPlain == plain
		// unchanged and continue to dedup on the real key.
		storedPlain = fmt.Sprintf("__enc_%d", t.ID)
	}
	res, err := s.exec(
		`UPDATE tokens SET plan_id=?, key=?, key_ciphertext=?, name=?, status=?, rpm=?, tpm=?, used_usd=?, models_whitelist=?, ip_whitelist=?, expires_at=? WHERE id=?`,
		t.PlanID, storedPlain, cipher, t.Name, int(t.Status), t.RPM, t.TPM, t.UsedUSD,
		encodeStrings(t.ModelsWhitelist), encodeStrings(t.IPWhitelist),
		toUnix(t.ExpiresAt), t.ID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *dbStore) IncrementTokenSpend(tokenID int64, amount float64) error {
	if amount == 0 {
		return nil
	}
	res, err := s.exec(
		`UPDATE tokens SET used_usd = used_usd + ? WHERE id = ?`, amount, tokenID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// IncrementPlanSpend atomically adds amount to a plan's used_usd,
// but only if the plan has either no budget (budget_usd = 0,
// meaning unlimited) or enough remaining headroom. The check and
// the update happen in a single SQL statement so concurrent
// requests cannot race past the budget. Returns ErrBudgetExceeded
// when the addition would overshoot the configured limit, and
// ErrNotFound when the plan row does not exist.
//
// Callers that pair this with IncrementTokenSpend must roll the
// token spend back on ErrBudgetExceeded to keep the two ledgers in
// agreement.
func (s *dbStore) IncrementPlanSpend(planID int64, amount float64) error {
	if amount == 0 || planID == 0 {
		return nil
	}
	res, err := s.exec(
		`UPDATE plans
		   SET used_usd = used_usd + ?
		 WHERE id = ?
		   AND (budget_usd = 0 OR used_usd + ? <= budget_usd)`,
		amount, planID, amount,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Distinguish "no such plan" from "budget would overflow".
		var exists int64
		if err := s.queryRow(`SELECT 1 FROM plans WHERE id = ?`, planID).Scan(&exists); err != nil {
			if err == sql.ErrNoRows {
				return ErrNotFound
			}
			return err
		}
		return ErrBudgetExceeded
	}
	return nil
}

// RecordRequestSpend credits both the token and plan ledgers
// inside a single SQL transaction. When the plan leg would
// exceed budget_usd the transaction is rolled back so the
// token ledger is automatically reverted — the caller never
// has to compensate manually.
//
// planID == 0 skips the plan leg (no plan bound to the token),
// in which case only the token ledger is touched and no
// ErrBudgetExceeded can be raised.
func (s *dbStore) RecordRequestSpend(tokenID, planID int64, amount float64) error {
	if amount == 0 {
		return nil
	}
	if tokenID == 0 {
		// No token leg to credit; nothing to do.
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	// tx.Rollback is safe to call after a successful Commit; the
	// deferred fallback ensures cleanup on any error path.
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(s.d.RewriteQuery(`UPDATE tokens SET used_usd = used_usd + ? WHERE id = ?`),
		amount, tokenID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}

	if planID > 0 {
		res, err = tx.Exec(s.d.RewriteQuery(`UPDATE plans
			   SET used_usd = used_usd + ?
			 WHERE id = ?
			   AND (budget_usd = 0 OR used_usd + ? <= budget_usd)`),
			amount, planID, amount,
		)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Distinguish "no such plan" from "budget would overflow".
			var exists int64
			if qerr := tx.QueryRow(s.d.RewriteQuery(`SELECT 1 FROM plans WHERE id = ?`), planID).Scan(&exists); qerr != nil {
				if qerr == sql.ErrNoRows {
					return ErrNotFound
				}
				return qerr
			}
			// Budget would overflow — deferred Rollback will
			// revert the token ledger.
			return ErrBudgetExceeded
		}
	}

	return tx.Commit()
}

// MarkTokenExpired flips a token's status to TokenExpired so the
// in-memory cache will skip it on the next reload and any stale
// request still holding the bearer will be rejected by the
// Expiry check in middleware. Idempotent.
func (s *dbStore) MarkTokenExpired(tokenID int64) error {
	_, err := s.exec(
		`UPDATE tokens SET status = ? WHERE id = ? AND status = ?`,
		int(model.TokenExpired), tokenID, int(model.TokenActive),
	)
	return err
}

func (s *dbStore) DeleteToken(id int64) error {
	_, err := s.exec(`DELETE FROM tokens WHERE id = ?`, id)
	return err
}

func scanTokenRow(s *dbStore, r interface {
	Scan(dest ...any) error
}) (*model.Token, error) {
	var t model.Token
	var status int
	var mwJSON, ipwJSON string
	var expires, lastUsed, created int64
	var plain, cipher string
	if err := r.Scan(&t.ID, &t.PlanID, &plain, &cipher, &t.Name, &status, &t.RPM, &t.TPM,
		&t.UsedUSD, &mwJSON, &ipwJSON, &expires, &lastUsed, &created, &t.UserID); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	t.Status = model.TokenStatus(status)
	t.ModelsWhitelist = decodeStrings(mwJSON)
	t.IPWhitelist = decodeStrings(ipwJSON)
	t.ExpiresAt = fromUnix(expires)
	t.LastUsedAt = fromUnix(lastUsed)
	t.CreatedAt = fromUnix(created)

	// Resolve plaintext key from ciphertext when a manager is
	// attached. If the row has only legacy plaintext, best-effort
	// upgrade to ciphertext form so subsequent reads go through
	// the manager.
	switch {
	case s.secrets != nil && cipher != "":
		pt, derr := s.secrets.Decrypt(cipher)
		if derr != nil {
			// Master-key mismatch or tampered ciphertext: leave
			// the token empty and let cache reload skip it.
			logging.Warn("token decrypt failed",
				logging.F("token_id", t.ID),
				logging.F("error", derr.Error()),
			)
			t.Key = ""
		} else {
			t.Key = string(pt)
		}
	case s.secrets != nil && plain != "":
		t.Key = plain
		if ct, eerr := s.secrets.Encrypt([]byte(plain)); eerr == nil {
			_, _ = s.exec(`UPDATE tokens SET key='', key_ciphertext=? WHERE id=?`, ct, t.ID)
		}
	default:
		t.Key = plain
	}
	return &t, nil
}

// ---------------- Plans ----------------
