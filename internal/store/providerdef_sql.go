package store

import (
	"time"

	"github.com/sn0wfree/llmRx/internal/dialect"
	"github.com/sn0wfree/llmRx/internal/model"
)

func (s *dbStore) GetProviderDefs() ([]model.ProviderDef, error) {
	rows, err := s.query(`SELECT id, name, display_name, protocol, base_url, created_at, updated_at FROM providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ProviderDef
	for rows.Next() {
		var p model.ProviderDef
		var created, updated int64
		if err := rows.Scan(&p.ID, &p.Name, &p.DisplayName, &p.Protocol, &p.BaseURL, &created, &updated); err != nil {
			return nil, err
		}
		p.CreatedAt = time.Unix(created, 0)
		p.UpdatedAt = time.Unix(updated, 0)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *dbStore) CreateProviderDef(p *model.ProviderDef) error {
	now := time.Now().Unix()
	pid, err := dialect.InsertOne(s.d, s.db, `INSERT INTO providers (name, display_name, protocol, base_url, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		p.Name, p.DisplayName, p.Protocol, p.BaseURL, now, now)
	if err != nil {
		return err
	}
	p.ID = pid
	p.CreatedAt = time.Unix(now, 0)
	p.UpdatedAt = time.Unix(now, 0)
	return nil
}

func (s *dbStore) DeleteProviderDef(id int64) error {
	_, err := s.exec(`DELETE FROM providers WHERE id = ?`, id)
	return err
}

// ---------- ComboModels ----------
