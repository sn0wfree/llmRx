package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type Client struct {
	baseURL    string
	authHdr    string
	httpClient *http.Client
	mu         sync.Mutex
	sessionID  string
}

func NewClient(baseURL, authHdr string) *Client {
	return &Client{
		baseURL: baseURL,
		authHdr: authHdr,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:    4,
				IdleConnTimeout: 60 * time.Second,
			},
		},
	}
}

func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	raw, err := c.rpc(ctx, "tools/list", nil)
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
	raw, err := c.rpc(ctx, "tools/call", params)
	if err != nil {
		return nil, err
	}
	var result ToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp: call tool decode: %w", err)
	}
	return &result, nil
}

func (c *Client) rpc(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
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
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mcp: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.authHdr != "" {
		httpReq.Header.Set("Authorization", c.authHdr)
	}
	httpResp, err := c.httpClient.Do(httpReq)
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

func (c *Client) Close() {
	c.httpClient.CloseIdleConnections()
}