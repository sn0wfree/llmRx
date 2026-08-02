package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientListTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Fatal("expected POST")
		}
		resp := NewResponse(1, map[string]any{
			"tools": []Tool{{Name: "test_tool", Description: "a test"}},
		})
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "test_tool" {
		t.Fatal("unexpected tools:", tools)
	}
}

func TestClientCallTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		if req.Method != "tools/call" {
			t.Fatal("expected tools/call")
		}
		params := req.Params
		if params["name"] != "echo" {
			t.Fatal("expected echo")
		}
		result := &ToolResult{
			Content: []ContentItem{{Type: "text", Text: "hello back"}},
		}
		b, _ := json.Marshal(result)
		var raw map[string]any
		json.Unmarshal(b, &raw)
		json.NewEncoder(w).Encode(NewResponse(1, raw))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	result, err := c.CallTool(context.Background(), "echo", map[string]any{"msg": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "hello back" {
		t.Fatal("unexpected result:", result)
	}
}

func TestClientRPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(NewError(1, CodeInternalError, "oops"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.ListTools(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestClientAuthHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatal("missing auth header")
		}
		json.NewEncoder(w).Encode(NewResponse(1, map[string]any{"tools": []Tool{}}))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "Bearer test-token")
	_, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
}

func TestClientHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.ListTools(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}
