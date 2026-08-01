package mcp

import (
	"context"
	"testing"

	"github.com/sn0wfree/llmRx/internal/store"
)

type mockRepo struct {
	store.MCPRepository
	servers []store.MCPServer
	tools   []store.MCPTool
}

func (m *mockRepo) GetMCPServer(ctx context.Context, id int64) (*store.MCPServer, error) {
	for _, s := range m.servers {
		if s.ID == id {
			return &s, nil
		}
	}
	return nil, nil
}

func (m *mockRepo) GetMCPTools(ctx context.Context, serverID int64) ([]store.MCPTool, error) {
	return m.tools, nil
}

func (m *mockRepo) SetMCPTools(ctx context.Context, serverID int64, tools []store.MCPTool) error {
	m.tools = tools
	return nil
}

func TestClientManagerGetClient(t *testing.T) {
	repo := &mockRepo{
		servers: []store.MCPServer{{ID: 1, Name: "test", URL: "http://localhost:9999", Enabled: true}},
	}
	mgr := NewClientManager(repo)
	_, err := mgr.GetClient(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
}

func TestClientManagerGetClientNonexistent(t *testing.T) {
	repo := &mockRepo{}
	mgr := NewClientManager(repo)
	c, err := mgr.GetClient(context.Background(), 999)
	if err != nil {
		t.Fatal(err)
	}
	if c != nil {
		t.Fatal("expected nil client")
	}
}

func TestClientManagerInvalidate(t *testing.T) {
	repo := &mockRepo{
		servers: []store.MCPServer{{ID: 1, Name: "test", URL: "http://localhost:9999", Enabled: true}},
	}
	mgr := NewClientManager(repo)
	c1, _ := mgr.GetClient(context.Background(), 1)
	if c1 == nil {
		t.Fatal("expected client")
	}
	mgr.Invalidate(1)
	c2, _ := mgr.GetClient(context.Background(), 1)
	if c2 == nil {
		t.Fatal("expected new client after invalidate")
	}
}

func TestClientManagerClose(t *testing.T) {
	repo := &mockRepo{
		servers: []store.MCPServer{{ID: 1, Name: "test", URL: "http://localhost:9999", Enabled: true}},
	}
	mgr := NewClientManager(repo)
	mgr.Close()
}

func TestClientManagerRefreshToolsNoServer(t *testing.T) {
	repo := &mockRepo{}
	mgr := NewClientManager(repo)
	tools, err := mgr.RefreshTools(context.Background(), 999)
	if err != nil {
		t.Fatal(err)
	}
	if tools != nil {
		t.Fatal("expected nil tools")
	}
}

func TestClientManagerDoubleGet(t *testing.T) {
	repo := &mockRepo{
		servers: []store.MCPServer{{ID: 1, Name: "test", URL: "http://localhost:9999", Enabled: true}},
	}
	mgr := NewClientManager(repo)
	c1, _ := mgr.GetClient(context.Background(), 1)
	c2, _ := mgr.GetClient(context.Background(), 1)
	if c1 != c2 {
		t.Fatal("expected same client instance")
	}
}