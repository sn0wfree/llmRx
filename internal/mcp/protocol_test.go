package mcp

import (
	"encoding/json"
	"testing"
)

func TestRequestResponse(t *testing.T) {
	req := &Request{JSONRPC: Version, ID: 1, Method: "tools/list"}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var got Request
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Method != "tools/list" || got.JSONRPC != Version {
		t.Fatalf("round-trip: %+v", got)
	}
}

func TestNewResponse(t *testing.T) {
	r := NewResponse(1, map[string]any{"ok": true})
	if r.Error != nil {
		t.Fatal("expected no error")
	}
	if r.Result["ok"] != true {
		t.Fatal("expected result")
	}
}

func TestNewError(t *testing.T) {
	r := NewError(1, CodeMethodNotFound, "not found")
	if r.Result != nil {
		t.Fatal("expected no result")
	}
	if r.Error == nil || r.Error.Code != CodeMethodNotFound {
		t.Fatal("expected error")
	}
}

func TestToolResult(t *testing.T) {
	tr := &ToolResult{
		Content: []ContentItem{{Type: "text", Text: "hello"}},
	}
	b, _ := json.Marshal(tr)
	var got ToolResult
	json.Unmarshal(b, &got)
	if len(got.Content) != 1 || got.Content[0].Text != "hello" {
		t.Fatal("tool result round-trip failed")
	}
}
