package store

import (
	"context"
	"errors"
	"time"

	"database/sql"
	"github.com/sn0wfree/llmRx/internal/dialect"
)

func (s *dbStore) GetMCPServers(ctx context.Context) ([]MCPServer, error) {
	rows, err := s.queryContext(ctx, `SELECT id, name, url, auth_header, transport, command, oauth_config_json, token_json, enabled, created_at FROM mcp_servers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPServer
	for rows.Next() {
		var srv MCPServer
		var created int64
		var enabled any
		if err := rows.Scan(&srv.ID, &srv.Name, &srv.URL, &srv.AuthHdr, &srv.Transport, &srv.Command, &srv.OAuthConfigJSON, &srv.TokenJSON, &enabled, &created); err != nil {
			return nil, err
		}
		srv.Enabled = s.d.ParseBool(enabled)
		srv.CreatedAt = fromUnix(created)
		out = append(out, srv)
	}
	return out, rows.Err()
}

func (s *dbStore) GetMCPServer(ctx context.Context, id int64) (*MCPServer, error) {
	row := s.queryRowContext(ctx, `SELECT id, name, url, auth_header, transport, command, oauth_config_json, token_json, enabled, created_at FROM mcp_servers WHERE id = ?`, id)
	var srv MCPServer
	var created int64
	var enabled any
	if err := row.Scan(&srv.ID, &srv.Name, &srv.URL, &srv.AuthHdr, &srv.Transport, &srv.Command, &srv.OAuthConfigJSON, &srv.TokenJSON, &enabled, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	srv.Enabled = s.d.ParseBool(enabled)
	srv.CreatedAt = fromUnix(created)
	return &srv, nil
}

func (s *dbStore) CreateMCPServer(ctx context.Context, srv *MCPServer) error {
	now := time.Now().Unix()
	if srv.Transport == "" {
		srv.Transport = "http"
	}
	srvID, err := dialect.InsertOneContext(s.d, s.db, ctx,
		`INSERT INTO mcp_servers (name, url, auth_header, transport, command, oauth_config_json, token_json, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		srv.Name, srv.URL, srv.AuthHdr, srv.Transport, srv.Command, srv.OAuthConfigJSON, srv.TokenJSON, s.d.Bool(srv.Enabled), now,
	)
	if err != nil {
		return err
	}
	srv.ID = srvID
	srv.CreatedAt = fromUnix(now)
	return nil
}

func (s *dbStore) UpdateMCPServer(ctx context.Context, srv *MCPServer) error {
	if srv.Transport == "" {
		srv.Transport = "http"
	}
	_, err := s.execContext(ctx,
		`UPDATE mcp_servers SET name=?, url=?, auth_header=?, transport=?, command=?, oauth_config_json=?, token_json=?, enabled=? WHERE id=?`,
		srv.Name, srv.URL, srv.AuthHdr, srv.Transport, srv.Command, srv.OAuthConfigJSON, srv.TokenJSON, s.d.Bool(srv.Enabled), srv.ID,
	)
	return err
}

func (s *dbStore) DeleteMCPServer(ctx context.Context, id int64) error {
	_, err := s.execContext(ctx, `DELETE FROM mcp_servers WHERE id = ?`, id)
	return err
}

func (s *dbStore) GetMCPTools(ctx context.Context, serverID int64) ([]MCPTool, error) {
	rows, err := s.queryContext(ctx,
		`SELECT id, server_id, name, description, input_schema_json FROM mcp_tools WHERE server_id = ? ORDER BY name`,
		serverID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPTool
	for rows.Next() {
		var t MCPTool
		if err := rows.Scan(&t.ID, &t.ServerID, &t.Name, &t.Description, &t.InputSchemaJSON); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *dbStore) SetMCPTools(ctx context.Context, serverID int64, tools []MCPTool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(s.d.RewriteQuery(`DELETE FROM mcp_tools WHERE server_id = ?`), serverID); err != nil {
		return err
	}
	for _, t := range tools {
		t.ServerID = serverID
		_, err := tx.Exec(s.d.RewriteQuery(`INSERT INTO mcp_tools (server_id, name, description, input_schema_json) VALUES (?, ?, ?, ?)`),
			serverID, t.Name, t.Description, t.InputSchemaJSON,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *dbStore) GetMCPToolPricing(ctx context.Context, toolID int64) (*MCPToolPricing, error) {
	row := s.queryRowContext(ctx, `SELECT mcp_tool_id, price_per_call_usd FROM mcp_tool_pricing WHERE mcp_tool_id = ?`, toolID)
	var p MCPToolPricing
	if err := row.Scan(&p.MCPToolID, &p.PricePerCallUSD); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &MCPToolPricing{MCPToolID: toolID}, nil
		}
		return nil, err
	}
	return &p, nil
}

func (s *dbStore) SetMCPToolPricing(ctx context.Context, p *MCPToolPricing) error {
	_, err := s.execContext(ctx,
		`INSERT INTO mcp_tool_pricing (mcp_tool_id, price_per_call_usd) VALUES (?, ?)
		 ON CONFLICT(mcp_tool_id) DO UPDATE SET price_per_call_usd = excluded.price_per_call_usd`,
		p.MCPToolID, p.PricePerCallUSD,
	)
	return err
}

func (s *dbStore) GetAllMCPTools(ctx context.Context) ([]MCPTool, error) {
	rows, err := s.queryContext(ctx,
		`SELECT t.id, t.server_id, t.name, t.description, t.input_schema_json
		 FROM mcp_tools t
		 JOIN mcp_servers s ON s.id = t.server_id
		 WHERE s.enabled = ?
		 ORDER BY t.name`,
		s.d.Bool(true),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPTool
	for rows.Next() {
		var t MCPTool
		if err := rows.Scan(&t.ID, &t.ServerID, &t.Name, &t.Description, &t.InputSchemaJSON); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *dbStore) GetEnabledMCPServers(ctx context.Context) ([]MCPServer, error) {
	rows, err := s.queryContext(ctx, `SELECT id, name, url, auth_header, transport, command, oauth_config_json, token_json, enabled, created_at FROM mcp_servers WHERE enabled = ? ORDER BY id`, s.d.Bool(true))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPServer
	for rows.Next() {
		var srv MCPServer
		var created int64
		var enabled any
		if err := rows.Scan(&srv.ID, &srv.Name, &srv.URL, &srv.AuthHdr, &srv.Transport, &srv.Command, &srv.OAuthConfigJSON, &srv.TokenJSON, &enabled, &created); err != nil {
			return nil, err
		}
		srv.Enabled = true
		srv.CreatedAt = fromUnix(created)
		out = append(out, srv)
	}
	return out, rows.Err()
}
