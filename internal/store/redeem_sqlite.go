package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/sn0wfree/llmRx/internal/dialect"
	"github.com/sn0wfree/llmRx/internal/model"
)

// Redeem-related errors, exported so API layers can map to HTTP.
var (
	ErrRedeemCodeNotFound   = errors.New("redeem code not found")
	ErrRedeemCodeUsed       = errors.New("redeem code already used")
	ErrRedeemCodeExpired    = errors.New("redeem code expired")
	ErrRedeemWrongKind      = errors.New("redeem code kind mismatch")
	ErrUsernameTaken        = errors.New("username already taken")
)

// scanRedeemCode maps one row to a RedeemCode.
func scanRedeemCode(row scanner) (*model.RedeemCode, error) {
	var c model.RedeemCode
	var kind string
	var usedAt, expAt, created int64
	if err := row.Scan(&c.ID, &c.Code, &kind, &c.AmountUSD, &c.InitialQuota,
		&c.Role, &c.CreatedBy, &expAt, &c.UsedBy, &usedAt, &created); err != nil {
		return nil, err
	}
	c.Kind = model.RedeemCodeKind(kind)
	c.ExpiresAt = fromUnix(expAt)
	c.UsedAt = fromUnix(usedAt)
	c.CreatedAt = fromUnix(created)
	return &c, nil
}

// CreateRedeemCode inserts one redeem code.
func (s *dbStore) CreateRedeemCode(c *model.RedeemCode) error {
	c.CreatedAt = time.Now().UTC()
	id, err := dialect.InsertOne(s.d, s.db,
		`INSERT INTO redeem_codes(code, kind, amount_usd, initial_quota, role, created_by, expires_at, used_by, used_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Code, string(c.Kind), c.AmountUSD, c.InitialQuota, int(c.Role),
		c.CreatedBy, toUnix(c.ExpiresAt), c.UsedBy, toUnix(c.UsedAt), toUnix(c.CreatedAt),
	)
	if err != nil {
		return err
	}
	c.ID = id
	return nil
}

// GetRedeemCodeByCode returns a code by its string form.
func (s *dbStore) GetRedeemCodeByCode(code string) (*model.RedeemCode, error) {
	row := s.queryRow(`SELECT id, code, kind, amount_usd, initial_quota, role, created_by, expires_at, used_by, used_at, created_at
		FROM redeem_codes WHERE code = ?`, code)
	c, err := scanRedeemCode(row)
	if err != nil {
		if errors.Is(err, errNoRows) {
			return nil, ErrRedeemCodeNotFound
		}
		return nil, err
	}
	return c, nil
}

// ListRedeemCodes returns all codes, newest first.
func (s *dbStore) ListRedeemCodes() ([]model.RedeemCode, error) {
	rows, err := s.query(`SELECT id, code, kind, amount_usd, initial_quota, role, created_by, expires_at, used_by, used_at, created_at
		FROM redeem_codes ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.RedeemCode
	for rows.Next() {
		c, err := scanRedeemCode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// UseInviteCode consumes an invite code and atomically creates the
// user, a dedicated token and a dedicated plan (budget =
// InitialQuota). The code is marked used inside the same
// transaction; a second redemption fails on the used_at check.
func (s *dbStore) UseInviteCode(ctx context.Context, code, username, passwordHash string) (*RegistrationResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Lock the code row for the transaction (SELECT ... FOR UPDATE
	// is Postgres-only; SQLite serializes writers anyway, but we
	// still re-check used_at inside the tx as the source of truth).
	var id, usedAt, expAt int64
	var kind string
	err = tx.QueryRow(s.d.RewriteQuery(
		`SELECT id, kind, used_at, expires_at FROM redeem_codes WHERE code = ?`),
		code).Scan(&id, &kind, &usedAt, &expAt)
	if err != nil {
		if errors.Is(err, errNoRows) {
			return nil, ErrRedeemCodeNotFound
		}
		return nil, err
	}
	if kind != string(model.RedeemKindInvite) {
		return nil, ErrRedeemWrongKind
	}
	if usedAt != 0 {
		return nil, ErrRedeemCodeUsed
	}
	if expAt != 0 && time.Now().After(fromUnix(expAt)) {
		return nil, ErrRedeemCodeExpired
	}

	// Initial quota + role come from the invite code row.
	var quota float64
	var role int
	if err := tx.QueryRow(s.d.RewriteQuery(
		`SELECT initial_quota, role FROM redeem_codes WHERE id = ?`), id).Scan(&quota, &role); err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	// 1. User.
	res, err := tx.Exec(s.d.RewriteQuery(`INSERT INTO users(username, password_hash, role, status, session_token, session_exp, created_at)
		 VALUES (?, ?, ?, ?, '', 0, ?)`),
		username, passwordHash, role, 1, toUnix(now))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrUsernameTaken
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}
	userID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	user := &model.User{ID: userID, Username: username, PasswordHash: passwordHash,
		Role: model.UserRole(role), Status: 1, CreatedAt: now}

	// 2. Plan.
	planName := "user-" + username
	res, err = tx.Exec(s.d.RewriteQuery(`INSERT INTO plans(name, budget_usd, used_usd, markup_ratio, status, created_at, updated_at)
		 VALUES (?, ?, 0, 1.0, 1, ?, ?)`),
		planName, quota, toUnix(now), toUnix(now))
	if err != nil {
		return nil, fmt.Errorf("insert plan: %w", err)
	}
	planID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	plan := &model.Plan{ID: planID, Name: planName, BudgetUSD: quota,
		MarkupRatio: 1.0, Status: 1, CreatedAt: now, UpdatedAt: now}

	// 3. Token (plaintext key column; encryption handled by the
	// store's normal CreateToken path would need a tx-aware variant,
	// so we mirror the sentinel logic here).
	tokenKey := newTokenKey()
	cipher := ""
	storedPlain := tokenKey
	if s.secrets != nil {
		ct, cerr := s.secrets.Encrypt([]byte(tokenKey))
		if cerr != nil {
			return nil, fmt.Errorf("encrypt token: %w", cerr)
		}
		cipher = ct
		storedPlain = "__enc_pending__"
	}
	res, err = tx.Exec(s.d.RewriteQuery(`INSERT INTO tokens(plan_id, key, key_ciphertext, name, status, rpm, tpm, used_usd, models_whitelist, ip_whitelist, expires_at, last_used_at, created_at)
		 VALUES (?, ?, ?, ?, ?, 0, 0, 0, '[]', '[]', 0, 0, ?)`),
		planID, storedPlain, cipher, username, 0, toUnix(now))
	if err != nil {
		return nil, fmt.Errorf("insert token: %w", err)
	}
	tokenID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if storedPlain == "__enc_pending__" {
		if _, err := tx.Exec(s.d.RewriteQuery(
			`UPDATE tokens SET key = ? WHERE id = ?`), "__enc_"+fmt.Sprint(tokenID), tokenID); err != nil {
			return nil, err
		}
	}
	token := &model.Token{ID: tokenID, PlanID: planID, Key: tokenKey,
		Name: username, Status: 0, CreatedAt: now}

	// 4. Mark the code used.
	if _, err := tx.Exec(s.d.RewriteQuery(
		`UPDATE redeem_codes SET used_by = ?, used_at = ? WHERE id = ? AND used_at = 0`),
		userID, toUnix(now), id); err != nil {
		return nil, err
	}
	// Verify the guarded UPDATE actually claimed it.
	var claimed int64
	if err := tx.QueryRow(s.d.RewriteQuery(
		`SELECT used_at FROM redeem_codes WHERE id = ?`), id).Scan(&claimed); err != nil {
		return nil, err
	}
	if claimed == 0 {
		return nil, ErrRedeemCodeUsed
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &RegistrationResult{User: user, Token: token, Plan: plan}, nil
}

// RedeemTopup adds amount_usd to the plan's budget and marks the
// code used, atomically. Returns the updated plan.
func (s *dbStore) RedeemTopup(ctx context.Context, code string, planID int64) (*model.Plan, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var id, usedAt, expAt int64
	var kind string
	if err := tx.QueryRow(s.d.RewriteQuery(
		`SELECT id, kind, used_at, expires_at FROM redeem_codes WHERE code = ?`), code).Scan(&id, &kind, &usedAt, &expAt); err != nil {
		if errors.Is(err, errNoRows) {
			return nil, ErrRedeemCodeNotFound
		}
		return nil, err
	}
	if kind != string(model.RedeemKindTopup) {
		return nil, ErrRedeemWrongKind
	}
	if usedAt != 0 {
		return nil, ErrRedeemCodeUsed
	}
	if expAt != 0 && time.Now().After(fromUnix(expAt)) {
		return nil, ErrRedeemCodeExpired
	}
	var amount float64
	if err := tx.QueryRow(s.d.RewriteQuery(
		`SELECT amount_usd FROM redeem_codes WHERE id = ?`), id).Scan(&amount); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if _, err := tx.Exec(s.d.RewriteQuery(
		`UPDATE plans SET budget_usd = budget_usd + ?, updated_at = ? WHERE id = ?`),
		amount, toUnix(now), planID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(s.d.RewriteQuery(
		`UPDATE redeem_codes SET used_by = ?, used_at = ? WHERE id = ? AND used_at = 0`),
		planID, toUnix(now), id); err != nil {
		return nil, err
	}
	var claimed int64
	if err := tx.QueryRow(s.d.RewriteQuery(
		`SELECT used_at FROM redeem_codes WHERE id = ?`), id).Scan(&claimed); err != nil {
		return nil, err
	}
	if claimed == 0 {
		return nil, ErrRedeemCodeUsed
	}

	var plan model.Plan
	if err := tx.QueryRow(s.d.RewriteQuery(
		`SELECT id, name, budget_usd, used_usd, markup_ratio, status FROM plans WHERE id = ?`), planID).
		Scan(&plan.ID, &plan.Name, &plan.BudgetUSD, &plan.UsedUSD, &plan.MarkupRatio, &plan.Status); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &plan, nil
}

// newTokenKey generates an OpenAI-style bearer key.
func newTokenKey() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 40)
	randBytes(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return "sk-" + string(b)
}

// scanner abstracts *sql.Row / *sql.Rows Scan.
type scanner interface {
	Scan(dest ...any) error
}

// errNoRows is sql.ErrNoRows; kept short for readability.
var errNoRows = sql.ErrNoRows

// isUniqueViolation reports whether err is a UNIQUE constraint
// violation (SQLite message or Postgres 23505 code).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, "UNIQUE constraint failed") {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return true
	}
	return false
}

// randBytes fills b with cryptographically random bytes.
func randBytes(b []byte) {
	_, _ = rand.Read(b)
}
