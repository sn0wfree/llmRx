package store

import (
	"errors"
	"fmt"
	"time"

	"database/sql"
	"github.com/sn0wfree/llmRx/internal/dialect"
	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/secrets"
)

func (s *dbStore) GetKeys(channelID int64) ([]model.Key, error) {
	rows, err := s.query(`SELECT id, channel_id, key, key_ciphertext, key_masked, status, last_used_at, created_at, balance, balance_currency, balance_updated_at, balance_error FROM keys WHERE channel_id = ? ORDER BY id`, channelID)
	if err != nil {
		return nil, err
	}
	type rawRow struct {
		k      model.Key
		plain  string
		cipher string
	}
	var raws []rawRow
	for rows.Next() {
		var rr rawRow
		var status, lastUsed, created int64
		var balanceUpdated sql.NullInt64
		if err := rows.Scan(&rr.k.ID, &rr.k.ChannelID, &rr.plain, &rr.cipher, &rr.k.KeyMasked, &status, &lastUsed, &created,
			&rr.k.Balance, &rr.k.BalanceCurrency, &balanceUpdated, &rr.k.BalanceError); err != nil {
			rows.Close()
			return nil, err
		}
		rr.k.Status = model.KeyStatus(status)
		rr.k.LastUsedAt = fromUnix(lastUsed)
		rr.k.CreatedAt = fromUnix(created)
		if balanceUpdated.Valid {
			t := fromUnix(balanceUpdated.Int64)
			rr.k.BalanceUpdatedAt = &t
		}
		raws = append(raws, rr)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	// Decrypt / migrate outside the row cursor: SetMaxOpenConns(1)
	// means a nested Exec would deadlock while the read connection
	// is still pinned. We do all writes in a second pass.
	out := make([]model.Key, 0, len(raws))
	badKeyIDs := make([]int64, 0)
	for _, rr := range raws {
		if s.secrets != nil {
			if rr.cipher != "" {
				pt, derr := s.secrets.Decrypt(rr.cipher)
				if derr != nil {
					// Master-key mismatch or tampered ciphertext:
					// mark this key as disabled and skip — the row
					// stays so the admin UI can prompt the user
					// to re-enter the API key. Returning an error
					// here would take the whole gateway down and
					// block the recovery path (no admin UI access).
					logging.Warn("key decrypt failed, marking disabled",
						logging.F("key_id", rr.k.ID),
						logging.F("error", derr.Error()),
					)
					rr.k.Status = model.KeyDisabled
					rr.k.Key = ""
					badKeyIDs = append(badKeyIDs, rr.k.ID)
					out = append(out, rr.k)
					continue
				}
				rr.k.Key = string(pt)
			} else if rr.plain != "" {
				// Legacy row: plaintext column has the value.
				rr.k.Key = rr.plain
				// Best-effort background migration to ciphertext.
				// Failures are retried on the next read.
				if ct, eerr := s.secrets.Encrypt([]byte(rr.plain)); eerr == nil {
					_, _ = s.exec(`UPDATE keys SET key='', key_ciphertext=? WHERE id=?`, ct, rr.k.ID)
				}
			}
		} else {
			// No secrets manager configured (legacy mode). If a row
			// carries ciphertext we cannot decode it, so disable
			// that key in-place rather than failing the whole load.
			if rr.cipher != "" {
				logging.Warn("key has ciphertext but no secrets manager, marking disabled",
					logging.F("key_id", rr.k.ID),
				)
				rr.k.Status = model.KeyDisabled
				rr.k.Key = ""
				badKeyIDs = append(badKeyIDs, rr.k.ID)
				out = append(out, rr.k)
				continue
			}
			rr.k.Key = rr.plain
		}
		out = append(out, rr.k)
	}
	if len(badKeyIDs) > 0 {
		logging.Warn("keys failed decrypt, action required",
			logging.F("bad_count", len(badKeyIDs)),
		)
	}
	return out, nil
}

func (s *dbStore) CreateKey(k *model.Key) error {
	k.CreatedAt = time.Now().UTC()
	plain := k.Key
	if plain == "" {
		return errors.New("key is empty")
	}
	cipher := ""
	storedPlain := plain // default for legacy mode
	if s.secrets != nil {
		ct, err := s.secrets.Encrypt([]byte(plain))
		if err != nil {
			return fmt.Errorf("encrypt key: %w", err)
		}
		cipher = ct
		storedPlain = "" // never store plaintext when a manager is attached
	}
	id, err := dialect.InsertOne(s.d, s.db,
		`INSERT INTO keys(channel_id, key, key_ciphertext, key_masked, status, last_used_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		k.ChannelID, storedPlain, cipher, k.KeyMasked, int(k.Status), toUnix(k.LastUsedAt), toUnix(k.CreatedAt),
	)
	if err != nil {
		return err
	}
	k.ID = id
	// Leave k.Key populated in-memory so callers (e.g. admin
	// response, pool seeding) get the plaintext immediately.
	return nil
}

func (s *dbStore) DeleteKey(id int64) error {
	_, err := s.exec(`DELETE FROM keys WHERE id = ?`, id)
	return err
}

// WipeKeys clears all key material in the keys table. The row
// shells (id, channel_id, key_masked, status, timestamps) are
// preserved so the admin UI can show "0 active keys" instead of
// orphan channels. Status is left untouched — callers that want
// to invalidate should set KeyDisabled explicitly. Used by the
// `-wipe-keys` recovery command after a master-key rotation
// renders existing ciphertext undecryptable.
func (s *dbStore) WipeKeys() (int64, error) {
	res, err := s.exec(`UPDATE keys SET key='', key_ciphertext='' WHERE key != '' OR key_ciphertext != ''`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *dbStore) ReencryptAllKeys(oldMgr, newMgr *secrets.Manager) (int, error) {
	rows, err := s.query(`SELECT id, key_ciphertext FROM keys WHERE key_ciphertext != ''`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type kr struct {
		id     int64
		cipher string
	}
	var keys []kr
	for rows.Next() {
		var r kr
		if err := rows.Scan(&r.id, &r.cipher); err != nil {
			return 0, err
		}
		keys = append(keys, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	rows.Close()

	for _, r := range keys {
		pt, err := oldMgr.Decrypt(r.cipher)
		if err != nil {
			return 0, fmt.Errorf("decrypt key %d: %w", r.id, err)
		}
		newCT, err := newMgr.Encrypt(pt)
		if err != nil {
			return 0, fmt.Errorf("encrypt key %d: %w", r.id, err)
		}
		if _, err := s.exec(`UPDATE keys SET key_ciphertext = ? WHERE id = ?`, newCT, r.id); err != nil {
			return 0, fmt.Errorf("update key %d: %w", r.id, err)
		}
	}
	return len(keys), nil
}

func (s *dbStore) RotateMasterKey(newKeyHex string) (int, error) {
	m, err := secrets.FromHexKey(newKeyHex)
	if err != nil {
		return 0, err
	}
	if s.secrets == nil {
		return 0, errors.New("no secrets manager configured; cannot rotate")
	}
	n, err := s.ReencryptAllKeys(s.secrets, m)
	if err != nil {
		return 0, err
	}
	tn, err := s.reencryptAllTokens(s.secrets, m)
	if err != nil {
		return n, err
	}
	s.secrets = m
	logging.Info("secrets rotated master key",
		logging.F("channel_keys", n),
		logging.F("tokens", tn),
	)
	return n + tn, nil
}

// reencryptAllTokens re-encrypts every tokens.key_ciphertext row
// from oldMgr to newMgr. Returns the count of tokens rotated.
// Mirror of ReencryptAllKeys but on the tokens table.
func (s *dbStore) reencryptAllTokens(oldMgr, newMgr *secrets.Manager) (int, error) {
	rows, err := s.query(`SELECT id, key_ciphertext FROM tokens WHERE key_ciphertext != ''`)
	if err != nil {
		return 0, err
	}
	type tr struct {
		id     int64
		cipher string
	}
	var toks []tr
	for rows.Next() {
		var r tr
		if err := rows.Scan(&r.id, &r.cipher); err != nil {
			rows.Close()
			return 0, err
		}
		toks = append(toks, r)
	}
	rows.Close()
	for _, r := range toks {
		pt, err := oldMgr.Decrypt(r.cipher)
		if err != nil {
			return 0, fmt.Errorf("decrypt token %d: %w", r.id, err)
		}
		newCT, err := newMgr.Encrypt(pt)
		if err != nil {
			return 0, fmt.Errorf("encrypt token %d: %w", r.id, err)
		}
		if _, err := s.exec(`UPDATE tokens SET key_ciphertext=? WHERE id=?`, newCT, r.id); err != nil {
			return 0, fmt.Errorf("update token %d: %w", r.id, err)
		}
	}
	return len(toks), nil
}

// ---------------- Tokens ----------------
