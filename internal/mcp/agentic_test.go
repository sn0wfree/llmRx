package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/store"
)

type mockProvider struct {
	responses []*provider.ChatResponse
	idx       int
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) Chat(ctx context.Context, req *provider.ChatRequest, key, baseURL string) (*provider.ChatResponse, int, error) {
	if m.idx >= len(m.responses) {
		return nil, 0, errors.New("no more responses")
	}
	resp := m.responses[m.idx]
	m.idx++
	return resp, 200, nil
}

type mockAgenticRepo struct {
	store.MCPRepository
	tools []store.MCPTool
}

func (m *mockAgenticRepo) GetAllMCPTools(ctx context.Context) ([]store.MCPTool, error) {
	return m.tools, nil
}

func TestAgenticLoopNoTools(t *testing.T) {
	repo := &mockAgenticRepo{}
	mgr := NewClientManager(repo)
	prov := &mockProvider{
		responses: []*provider.ChatResponse{
			{Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: "hi"}}}},
		},
	}
	loop := NewAgenticLoop(mgr, repo, prov)
	resp, err := loop.Execute(context.Background(), &provider.ChatRequest{Model: "test"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.ContentString() != "hi" {
		t.Fatal("unexpected response")
	}
}

func TestAgenticLoopHasTools(t *testing.T) {
	repo := &mockAgenticRepo{tools: []store.MCPTool{{Name: "test"}}}
	mgr := NewClientManager(repo)
	loop := NewAgenticLoop(mgr, repo, nil)
	has, err := loop.HasTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("expected has tools")
	}
}

func TestAgenticLoopHasNoTools(t *testing.T) {
	repo := &mockAgenticRepo{}
	mgr := NewClientManager(repo)
	loop := NewAgenticLoop(mgr, repo, nil)
	has, err := loop.HasTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("expected no tools")
	}
}

func TestAgenticLoopProviderError(t *testing.T) {
	repo := &mockAgenticRepo{tools: []store.MCPTool{{Name: "test", ServerID: 1}}}
	mgr := NewClientManager(repo)
	prov := &mockProvider{}
	loop := NewAgenticLoop(mgr, repo, prov)
	_, err := loop.Execute(context.Background(), &provider.ChatRequest{Model: "test"}, "", "")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestAgenticLoopNoToolCalls(t *testing.T) {
	repo := &mockAgenticRepo{tools: []store.MCPTool{{Name: "test", ServerID: 1}}}
	mgr := NewClientManager(repo)
	prov := &mockProvider{
		responses: []*provider.ChatResponse{
			{Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: "no tool calls"}}}},
		},
	}
	loop := NewAgenticLoop(mgr, repo, prov)
	resp, err := loop.Execute(context.Background(), &provider.ChatRequest{Model: "test"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Choices[0].Message.ContentString() != "no tool calls" {
		t.Fatal("unexpected response")
	}
}

func TestAgenticLoopEmptyChoices(t *testing.T) {
	repo := &mockAgenticRepo{tools: []store.MCPTool{{Name: "test", ServerID: 1}}}
	mgr := NewClientManager(repo)
	prov := &mockProvider{
		responses: []*provider.ChatResponse{
			{Choices: []provider.Choice{}},
		},
	}
	loop := NewAgenticLoop(mgr, repo, prov)
	resp, err := loop.Execute(context.Background(), &provider.ChatRequest{Model: "test"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Choices) != 0 {
		t.Fatal("expected empty choices")
	}
}