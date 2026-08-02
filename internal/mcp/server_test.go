package mcp

import (
	"context"
	"testing"
)

func TestServerListTools(t *testing.T) {
	s := NewServer(DefaultTools(), func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true}, nil
	})
	req := &Request{JSONRPC: Version, ID: 1, Method: "tools/list"}
	resp := s.HandleRequest(context.Background(), req)
	if resp.Error != nil {
		t.Fatal("unexpected error:", resp.Error)
	}
	toolsRaw, ok := resp.Result["tools"]
	if !ok {
		t.Fatal("missing tools")
	}
	tools, ok := toolsRaw.([]Tool)
	if !ok {
		t.Fatal("tools is not []Tool, got", toolsRaw)
	}
	if len(tools) != 2 {
		t.Fatal("expected 2 tools, got", len(tools))
	}
}

func TestServerCallTool(t *testing.T) {
	s := NewServer(DefaultTools(), func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		if name == "test_tool" {
			return map[string]any{"result": "done"}, nil
		}
		return nil, nil
	})
	req := &Request{
		JSONRPC: Version,
		ID:      2,
		Method:  "tools/call",
		Params:  map[string]any{"name": "test_tool", "arguments": map[string]any{}},
	}
	resp := s.HandleRequest(context.Background(), req)
	if resp.Error != nil {
		t.Fatal("unexpected error:", resp.Error)
	}
	if resp.Result["result"] != "done" {
		t.Fatal("expected result")
	}
}

func TestServerUnknownMethod(t *testing.T) {
	s := NewServer(DefaultTools(), nil)
	req := &Request{JSONRPC: Version, ID: 1, Method: "bogus"}
	resp := s.HandleRequest(context.Background(), req)
	if resp.Error == nil {
		t.Fatal("expected error")
	}
}

func TestServerHandleHTTP(t *testing.T) {
	s := NewServer(DefaultTools(), nil)
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	resp := s.HandleHTTP(context.Background(), body)
	if resp.Error != nil {
		t.Fatal("unexpected error:", resp.Error)
	}
}

func TestServerHandleHTTPBadJSON(t *testing.T) {
	s := NewServer(DefaultTools(), nil)
	body := []byte(`{bad json`)
	resp := s.HandleHTTP(context.Background(), body)
	if resp.Error == nil {
		t.Fatal("expected error")
	}
}

func TestServerHandleHTTPBadVersion(t *testing.T) {
	s := NewServer(DefaultTools(), nil)
	body := []byte(`{"jsonrpc":"1.0","id":1,"method":"tools/list"}`)
	resp := s.HandleHTTP(context.Background(), body)
	if resp.Error == nil {
		t.Fatal("expected error for bad version")
	}
}

func TestServerHandleHTTPNoMethod(t *testing.T) {
	s := NewServer(DefaultTools(), nil)
	body := []byte(`{"jsonrpc":"2.0","id":1}`)
	resp := s.HandleHTTP(context.Background(), body)
	if resp.Error == nil {
		t.Fatal("expected error for missing method")
	}
}

func TestServerTools(t *testing.T) {
	s := NewServer(DefaultTools(), nil)
	tools := s.Tools()
	if len(tools) != 2 {
		t.Fatal("expected 2 default tools")
	}
}

func TestMarshalToolResult(t *testing.T) {
	m := MarshalToolResult("hello", false)
	if m["isError"] == true {
		t.Fatal("expected isError=false")
	}
	content, ok := m["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatal("expected content array")
	}
	item := content[0].(map[string]any)
	if item["text"] != "hello" {
		t.Fatal("expected text=hello")
	}
}

func TestServerCallToolUnknownTool(t *testing.T) {
	s := NewServer(DefaultTools(), func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		return nil, nil
	})
	req := &Request{
		JSONRPC: Version,
		ID:      1,
		Method:  "tools/call",
		Params:  map[string]any{"name": "nonexistent", "arguments": map[string]any{}},
	}
	resp := s.HandleRequest(context.Background(), req)
	if resp.Error != nil {
		t.Fatal("expected no error - handler returns nil,nil for unknown tools")
	}
}
