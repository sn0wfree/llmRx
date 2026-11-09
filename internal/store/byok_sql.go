package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"database/sql"
	"github.com/sn0wfree/llmRx/internal/dialect"
	"github.com/sn0wfree/llmRx/internal/model"
)

func (s *dbStore) CreateBYOKChannel(ctx context.Context, ch *model.BYOKChannel) (int64, error) {
	if ch.Provider == "" {
		return 0, errors.New("provider is required")
	}
	if ch.KeyCiphertext == "" && ch.KeyMasked == "" {
		return 0, errors.New("key is required")
	}
	now := time.Now().Unix()
	id, err := dialect.InsertOneContext(s.d, s.db, ctx, `
		INSERT INTO byok_channels
		  (provider, key_ciphertext, key_masked, owner_ip, owner_email,
		   status, last_used_at, use_count, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ch.Provider, ch.KeyCiphertext, ch.KeyMasked, ch.OwnerIP, ch.OwnerEmail,
		ch.Status, ch.LastUsedAt.Unix(), ch.UseCount, ch.ExpiresAt.Unix(), now)
	if err != nil {
		return 0, fmt.Errorf("insert byok channel: %w", err)
	}
	return id, nil
}

func (s *dbStore) ListBYOKChannels(ctx context.Context) ([]*model.BYOKChannel, error) {
	rows, err := s.queryContext(ctx, `
		SELECT id, provider, key_ciphertext, key_masked, owner_ip, owner_email,
		       status, last_used_at, use_count, expires_at, created_at
		FROM byok_channels
		ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("query byok channels: %w", err)
	}
	defer rows.Close()
	var out []*model.BYOKChannel
	for rows.Next() {
		c, err := scanBYOKRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *dbStore) GetBYOKChannel(ctx context.Context, id int64) (*model.BYOKChannel, error) {
	row := s.queryRowContext(ctx, `
		SELECT id, provider, key_ciphertext, key_masked, owner_ip, owner_email,
		       status, last_used_at, use_count, expires_at, created_at
		FROM byok_channels WHERE id = ?`, id)
	c, err := scanBYOKRow(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return c, err
}

// GetBYOKChannelByIP looks up an active BYOK row by client IP. Used
// by the UnknownTokenHook to find a previously registered consumer key.
func (s *dbStore) GetBYOKChannelByIP(ctx context.Context, ownerIP string) (*model.BYOKChannel, error) {
	row := s.queryRowContext(ctx, `
		SELECT id, provider, key_ciphertext, key_masked, owner_ip, owner_email,
		       status, last_used_at, use_count, expires_at, created_at
		FROM byok_channels
		WHERE owner_ip = ? AND status = 1
		ORDER BY id DESC LIMIT 1`, ownerIP)
	c, err := scanBYOKRow(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return c, err
}

// TouchBYOKChannel increments use_count and updates last_used_at.
func (s *dbStore) TouchBYOKChannel(ctx context.Context, id int64) error {
	_, err := s.execContext(ctx, `
		UPDATE byok_channels
		   SET use_count = use_count + 1, last_used_at = ?
		 WHERE id = ?`, time.Now().Unix(), id)
	return err
}

func (s *dbStore) DeleteBYOKChannel(ctx context.Context, id int64) error {
	res, err := s.execContext(ctx, `DELETE FROM byok_channels WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete byok channel: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// scanBYOKRow scans one row into a BYOKChannel. Accepts any type
// implementing Scan (both *sql.Row and *sql.Rows).
func scanBYOKRow(r interface {
	Scan(dest ...any) error
}) (*model.BYOKChannel, error) {
	var (
		c         model.BYOKChannel
		lastUsed  int64
		expiresAt int64
		createdAt int64
	)
	err := r.Scan(&c.ID, &c.Provider, &c.KeyCiphertext, &c.KeyMasked,
		&c.OwnerIP, &c.OwnerEmail, &c.Status, &lastUsed, &c.UseCount,
		&expiresAt, &createdAt)
	if err != nil {
		return nil, err
	}
	c.LastUsedAt = time.Unix(lastUsed, 0)
	c.ExpiresAt = time.Unix(expiresAt, 0)
	c.CreatedAt = time.Unix(createdAt, 0)
	return &c, nil
}

// ---------- ProviderDefs ----------
