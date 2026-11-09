package store

import (
	"time"

	"database/sql"
	"github.com/sn0wfree/llmRx/internal/dialect"
	"github.com/sn0wfree/llmRx/internal/model"
)

func (s *dbStore) GetUsers() ([]model.User, error) {
	rows, err := s.query(`SELECT id, username, password_hash, role, status, session_token, session_exp, created_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func (s *dbStore) GetUser(id int64) (*model.User, error) {
	row := s.queryRow(`SELECT id, username, password_hash, role, status, session_token, session_exp, created_at FROM users WHERE id = ?`, id)
	return scanUser(row)
}

func (s *dbStore) GetUserByUsername(username string) (*model.User, error) {
	row := s.queryRow(`SELECT id, username, password_hash, role, status, session_token, session_exp, created_at FROM users WHERE username = ?`, username)
	return scanUser(row)
}

func (s *dbStore) GetUserBySession(token string) (*model.User, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	now := time.Now().UTC().UnixMilli()
	row := s.queryRow(
		`SELECT id, username, password_hash, role, status, session_token, session_exp, created_at FROM users
		 WHERE session_token = ? AND status = 1 AND (session_exp = 0 OR session_exp > ?)`,
		token, now,
	)
	return scanUser(row)
}

func (s *dbStore) CreateUser(u *model.User) error {
	u.CreatedAt = time.Now().UTC()
	id, err := dialect.InsertOne(s.d, s.db,
		`INSERT INTO users(username, password_hash, role, status, session_token, session_exp, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		u.Username, u.PasswordHash, int(u.Role), u.Status, u.SessionToken, sessionExpUnix(u.SessionExp), toUnix(u.CreatedAt),
	)
	if err != nil {
		return err
	}
	u.ID = id
	return nil
}

func (s *dbStore) UpdateUser(u *model.User) error {
	_, err := s.exec(
		`UPDATE users SET password_hash=?, role=?, status=?, session_token=?, session_exp=? WHERE id=?`,
		u.PasswordHash, int(u.Role), u.Status, u.SessionToken, sessionExpUnix(u.SessionExp), u.ID,
	)
	return err
}

// CleanupExpiredSessions clears session_token for users whose
// session_exp is set and in the past. Returns rows affected.
func (s *dbStore) CleanupExpiredSessions() (int64, error) {
	now := time.Now().UTC().UnixMilli()
	res, err := s.exec(
		`UPDATE users SET session_token = '' WHERE session_exp > 0 AND session_exp <= ?`,
		now,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func sessionExpUnix(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.UnixMilli()
}

func scanUser(r interface {
	Scan(dest ...any) error
}) (*model.User, error) {
	var u model.User
	var role int
	var sessionExp, created int64
	if err := r.Scan(&u.ID, &u.Username, &u.PasswordHash, &role, &u.Status, &u.SessionToken, &sessionExp, &created); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.Role = model.UserRole(role)
	u.CreatedAt = fromUnix(created)
	if sessionExp > 0 {
		exp := time.UnixMilli(sessionExp).UTC()
		u.SessionExp = &exp
	}
	return &u, nil
}

// ---------------- alerts ----------------
