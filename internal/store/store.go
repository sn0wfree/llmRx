package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/sn0wfree/llmRx/internal/secrets"
)

type MCPServer struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	AuthHdr   string    `json:"auth_header"`
	// Transport is the wire protocol: "http" (default) or "stdio".
	Transport string    `json:"transport"`
	// Command is the shell command for stdio servers
	// (e.g. "npx @modelcontextprotocol/server-github").
	Command   string    `json:"command"`
	// OAuthConfigJSON holds the OAuth client config
	// ({"client_id":...,"client_secret":...,"scopes":[...]}). Empty
	// means the server requires no OAuth (static auth_header only).
	OAuthConfigJSON string `json:"oauth_config_json"`
	// TokenJSON stores the persisted OAuth token set, encrypted via
	// the secrets manager when one is attached. Empty when not
	// authorized.
	TokenJSON string    `json:"token_json"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

type MCPTool struct {
	ID              int64  `json:"id"`
	ServerID        int64  `json:"server_id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	InputSchemaJSON string `json:"input_schema_json"`
}

type MCPToolPricing struct {
	MCPToolID       int64   `json:"mcp_tool_id"`
	PricePerCallUSD float64 `json:"price_per_call_usd"`
}

type MCPRepository interface {
	GetMCPServers(ctx context.Context) ([]MCPServer, error)
	GetMCPServer(ctx context.Context, id int64) (*MCPServer, error)
	CreateMCPServer(ctx context.Context, s *MCPServer) error
	UpdateMCPServer(ctx context.Context, s *MCPServer) error
	DeleteMCPServer(ctx context.Context, id int64) error
	GetMCPTools(ctx context.Context, serverID int64) ([]MCPTool, error)
	SetMCPTools(ctx context.Context, serverID int64, tools []MCPTool) error
	GetMCPToolPricing(ctx context.Context, toolID int64) (*MCPToolPricing, error)
	SetMCPToolPricing(ctx context.Context, p *MCPToolPricing) error
	GetAllMCPTools(ctx context.Context) ([]MCPTool, error)
	GetEnabledMCPServers(ctx context.Context) ([]MCPServer, error)
}

type Store interface {
	ChannelRepository
	KeyRepository
	TokenRepository
	PlanRepository
	UserRepository
	AlertRepository
	GuardrailRepository
	BYOKRepository
	ProviderDefRepository
	ComboModelRepository
	RuntimeRepository
	SecurityRepository
	MCPRepository

	Ping(ctx context.Context) error
	Close() error

	RawQueryRow(query string, args ...any) *sql.Row
	RawQuery(query string, args ...any) (*sql.Rows, error)
	RawDB() *sql.DB
}

// SecretsProvider is implemented by stores that expose their secrets
// manager (SQLite). Other backends may return nil.
type SecretsProvider interface {
	SecretsManager() *secrets.Manager
}

type DrainedChannel struct {
	ID   int64
	Name string
}