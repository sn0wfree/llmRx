package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// transport is the wire layer between the MCP client and a server.
// httpTransport talks HTTP+SSE to a remote endpoint; stdioTransport
// spawns a local sidecar process and exchanges JSON-RPC over
// stdin/stdout.
type transport interface {
	rpc(ctx context.Context, method string, params map[string]any) (json.RawMessage, error)
	Close() error
}

type httpTransport struct {
	baseURL    string
	authHdr    string
	httpClient *http.Client
}

// Client is the MCP client facade used by the agentic loop. It
// delegates the wire layer to a transport, so the same ListTools /
// CallTool API works for HTTP and stdio servers.
type Client struct {
	tr transport
}

func NewClient(baseURL, authHdr string) *Client {
	return &Client{tr: &httpTransport{
		baseURL: baseURL,
		authHdr: authHdr,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:    4,
				IdleConnTimeout: 60 * time.Second,
			},
		},
	}}
}

// NewClientWithTransport builds a Client around a custom transport
// (used by stdio and OAuth-aware HTTP transports).
func NewClientWithTransport(tr transport) *Client {
	return &Client{tr: tr}
}

func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	raw, err := c.tr.rpc(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var wrapper struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, fmt.Errorf("mcp: list tools decode: %w", err)
	}
	return wrapper.Tools, nil
}

func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	params := map[string]any{"name": name, "arguments": args}
	raw, err := c.tr.rpc(ctx, "tools/call", params)
	if err != nil {
		return nil, err
	}
	var result ToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp: call tool decode: %w", err)
	}
	return &result, nil
}

// Close releases the underlying transport.
func (c *Client) Close() error {
	if c.tr != nil {
		return c.tr.Close()
	}
	return nil
}

// rpc sends one JSON-RPC request over HTTP and decodes the result.
func (t *httpTransport) rpc(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	req := &Request{
		JSONRPC: Version,
		ID:      1,
		Method:  method,
		Params:  params,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mcp: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if t.authHdr != "" {
		httpReq.Header.Set("Authorization", t.authHdr)
	}
	httpResp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("mcp: http do: %w", err)
	}
	defer httpResp.Body.Close()
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("mcp: read body: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mcp: http %d: %s", httpResp.StatusCode, string(respBody))
	}
	return decodeResponse(respBody)
}

// decodeResponse parses a JSON-RPC response payload, returning the
// raw result or an error for JSON-RPC level errors.
func decodeResponse(respBody []byte) (json.RawMessage, error) {
	var resp Response
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("mcp: decode response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("mcp: rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	result, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal result: %w", err)
	}
	return result, nil
}

func (t *httpTransport) Close() error {
	t.httpClient.CloseIdleConnections()
	return nil
}
