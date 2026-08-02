package mcp

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/store"
)

type MCPServer struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	AuthHdr   string    `json:"auth_header"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

type MCPToolPricing struct {
	MCPToolID       int64   `json:"mcp_tool_id"`
	PricePerCallUSD float64 `json:"price_per_call_usd"`
}

type ClientManager struct {
	mu      sync.RWMutex
	clients map[int64]*Client
	repo    store.MCPRepository
	oauth   *OAuthManager // optional; nil = no OAuth support
}

func NewClientManager(repo store.MCPRepository) *ClientManager {
	return &ClientManager{
		clients: make(map[int64]*Client),
		repo:    repo,
	}
}

// SetOAuthManager attaches the OAuth manager used to supply tokens
// for OAuth-configured MCP servers.
func (m *ClientManager) SetOAuthManager(o *OAuthManager) { m.oauth = o }

// OAuth returns the attached OAuth manager (may be nil).
func (m *ClientManager) OAuth() *OAuthManager { return m.oauth }

func (m *ClientManager) GetClient(ctx context.Context, serverID int64) (*Client, error) {
	m.mu.RLock()
	c, ok := m.clients[serverID]
	m.mu.RUnlock()
	if ok {
		return c, nil
	}
	// Prepare outside the lock: the DB lookup and client
	// construction (spawning a stdio child) are slow, and holding
	// the mutex across them serialized every MCP client creation.
	srv, err := m.repo.GetMCPServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if srv == nil {
		return nil, nil
	}
	var fresh *Client
	if srv.Transport == "stdio" {
		if srv.Command == "" {
			return nil, fmt.Errorf("mcp: stdio server %s has empty command", srv.Name)
		}
		fresh = NewStdioClient(srv.Command)
	} else if m.oauth != nil && m.oauth.NeedsOAuth(srv) {
		tr := &httpTransport{
			baseURL:    srv.URL,
			httpClient: &http.Client{Timeout: 30 * time.Second},
		}
		tr.tokenProvider = m.oauth.TokenProvider(srv.ID)
		fresh = NewClientWithTransport(tr)
	} else {
		fresh = NewClient(srv.URL, srv.AuthHdr)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.clients[serverID]; ok {
		// Someone else won the race. Discard fresh — stdio
		// transports spawn their child lazily on first use, so
		// nothing leaks here.
		return c, nil
	}
	m.clients[serverID] = fresh
	return fresh, nil
}

func (m *ClientManager) RefreshTools(ctx context.Context, serverID int64) ([]store.MCPTool, error) {
	c, err := m.GetClient(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, nil
	}
	tools, err := c.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	mtools := make([]store.MCPTool, 0, len(tools))
	for _, t := range tools {
		schemaJSON := "{}"
		if t.InputSchema != nil {
			b, _ := Marshal(t.InputSchema)
			schemaJSON = string(b)
		}
		mtools = append(mtools, store.MCPTool{
			ServerID:        serverID,
			Name:            t.Name,
			Description:     t.Description,
			InputSchemaJSON: schemaJSON,
		})
	}
	if err := m.repo.SetMCPTools(ctx, serverID, mtools); err != nil {
		return nil, err
	}
	return mtools, nil
}

func (m *ClientManager) Invalidate(serverID int64) {
	m.mu.Lock()
	delete(m.clients, serverID)
	m.mu.Unlock()
}

func (m *ClientManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clients {
		c.Close()
	}
	m.clients = make(map[int64]*Client)
}
