package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	tools     []store.MCPTool
	server    *store.MCPServer
	pricing   map[int64]*store.MCPToolPricing
	serverCalls int
	pricingCalls int
}

func (m *mockAgenticRepo) GetAllMCPTools(ctx context.Context) ([]store.MCPTool, error) {
	return m.tools, nil
}

func (m *mockAgenticRepo) GetMCPServer(ctx context.Context, id int64) (*store.MCPServer, error) {
	m.serverCalls++
	return m.server, nil
}

func (m *mockAgenticRepo) GetMCPToolPricing(ctx context.Context, toolID int64) (*store.MCPToolPricing, error) {
	m.pricingCalls++
	if m.pricing == nil {
		return nil, nil
	}
	return m.pricing[toolID], nil
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
	resp, usage, err := loop.Execute(context.Background(), &provider.ChatRequest{Model: "test"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.ContentString() != "hi" {
		t.Fatal("unexpected response")
	}
	if usage.TotalCalls() != 0 {
		t.Fatal("expected no tool calls")
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
	_, _, err := loop.Execute(context.Background(), &provider.ChatRequest{Model: "test"}, "", "")
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
	resp, _, err := loop.Execute(context.Background(), &provider.ChatRequest{Model: "test"}, "", "")
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
	resp, _, err := loop.Execute(context.Background(), &provider.ChatRequest{Model: "test"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Choices) != 0 {
		t.Fatal("expected empty choices")
	}
}

func TestMCPUsageHelpers(t *testing.T) {
	u := MCPUsage{Calls: []MCPCall{
		{Name: "tool1", ServerName: "server1", CostUSD: 0.5},
		{Name: "tool2", ServerName: "server2", CostUSD: 1.5},
		{Name: "tool3", ServerName: "server1", CostUSD: 0.0},
	}}
	if u.TotalCalls() != 3 {
		t.Fatalf("expected 3 calls, got %d", u.TotalCalls())
	}
	if u.TotalCostUSD() != 2.0 {
		t.Fatalf("expected 2.0 USD, got %f", u.TotalCostUSD())
	}
	names := u.ServerNames()
	if len(names) != 2 || names[0] != "server1" || names[1] != "server2" {
		t.Fatalf("unexpected server names: %v", names)
	}
	empty := MCPUsage{}
	if empty.TotalCalls() != 0 || empty.TotalCostUSD() != 0 {
		t.Fatal("expected empty usage")
	}
	if len(empty.ServerNames()) != 0 {
		t.Fatal("expected empty server names")
	}
	// ToolName convenience.
	c := MCPCall{Name: "my_tool"}
	if c.ToolName() != "my_tool" {
		t.Fatal("ToolName mismatch")
	}
}

func TestMCPUsageEmpty(t *testing.T) {
	u := MCPUsage{}
	if u.TotalCalls() != 0 {
		t.Fatal("expected 0")
	}
	if u.TotalCostUSD() != 0 {
		t.Fatal("expected 0")
	}
	if len(u.ServerNames()) != 0 {
		t.Fatal("expected empty")
	}
}

func TestResolveToolsNoTools(t *testing.T) {
	repo := &mockAgenticRepo{}
	mgr := NewClientManager(repo)
	prov := &mockProvider{}
	loop := NewAgenticLoop(mgr, repo, prov)
	req := &provider.ChatRequest{
		Model: "test",
		Messages: []provider.Message{
			{Role: "user", Content: "hi"},
		},
	}
	msgs, usage, err := loop.ResolveTools(context.Background(), req, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatal("expected messages unchanged")
	}
	if usage.TotalCalls() != 0 {
		t.Fatal("expected no tool calls")
	}
}

func TestResolveToolsNoToolCallsInMessages(t *testing.T) {
	repo := &mockAgenticRepo{tools: []store.MCPTool{{Name: "test", ServerID: 1}}}
	mgr := NewClientManager(repo)
	prov := &mockProvider{
		responses: []*provider.ChatResponse{
			{Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: "final answer"}}}},
		},
	}
	loop := NewAgenticLoop(mgr, repo, prov)
	req := &provider.ChatRequest{
		Model: "test",
		Messages: []provider.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "no tool calls here"},
		},
	}
	msgs, usage, err := loop.ResolveTools(context.Background(), req, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected messages")
	}
	if usage.TotalCalls() != 0 {
		t.Fatal("expected no tool calls")
	}
}

func TestResolveToolsWithToolCalls(t *testing.T) {
	tool := store.MCPTool{ID: 1, Name: "get_weather", ServerID: 1}
	repo := &mockAgenticRepo{
		tools:  []store.MCPTool{tool},
		server: &store.MCPServer{Name: "weather-server"},
		pricing: map[int64]*store.MCPToolPricing{
			1: {PricePerCallUSD: 0.01},
		},
	}
	mgr := NewClientManager(repo)
	prov := &mockProvider{
		responses: []*provider.ChatResponse{
			{Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: "final answer"}}}},
		},
	}
	loop := NewAgenticLoop(mgr, repo, prov)
	req := &provider.ChatRequest{
		Model: "test",
		Messages: []provider.Message{
			{Role: "user", Content: "what's the weather?"},
			{Role: "assistant", ToolCalls: []provider.ToolCall{
				{ID: "call_1", Function: provider.FunctionCall{Name: "get_weather", Arguments: `{"loc":"SF"}`}},
			}},
		},
	}
	msgs, usage, err := loop.ResolveTools(context.Background(), req, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected messages")
	}
	// The tool call should be dispatched; client returns nil since
	// GetMCPServer returns nil server (no client), so it's an error
	// tool call but still counted.
	if usage.TotalCalls() != 1 {
		t.Fatalf("expected 1 tool call, got %d", usage.TotalCalls())
	}
	if usage.Calls[0].ServerName != "weather-server" {
		t.Fatalf("expected server name weather-server, got %q", usage.Calls[0].ServerName)
	}
	// Pricing is only looked up for successful calls. The tool call
	// failed (no client URL), so cost is 0.
	if usage.Calls[0].CostUSD != 0 {
		t.Fatalf("expected cost 0 for failed call, got %f", usage.Calls[0].CostUSD)
	}
	// Verify the tool call was counted.
	if usage.TotalCalls() != 1 {
		t.Fatalf("expected 1 tool call, got %d", usage.TotalCalls())
	}
}

func TestResolveToolsSuccessfulCallWithPricing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := &ToolResult{
			Content: []ContentItem{{Type: "text", Text: "weather: sunny"}},
		}
		b, _ := json.Marshal(result)
		var raw map[string]any
		json.Unmarshal(b, &raw)
		json.NewEncoder(w).Encode(NewResponse(1, raw))
	}))
	defer srv.Close()

	tool := store.MCPTool{ID: 1, Name: "get_weather", ServerID: 1}
	repo := &mockAgenticRepo{
		tools: []store.MCPTool{tool},
		server: &store.MCPServer{
			Name:    "weather-server",
			URL:     srv.URL,
			AuthHdr: "",
		},
		pricing: map[int64]*store.MCPToolPricing{
			1: {PricePerCallUSD: 0.05},
		},
	}
	mgr := NewClientManager(repo)
	prov := &mockProvider{
		responses: []*provider.ChatResponse{
			{Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: "final answer"}}}},
		},
	}
	loop := NewAgenticLoop(mgr, repo, prov)
	req := &provider.ChatRequest{
		Model: "test",
		Messages: []provider.Message{
			{Role: "user", Content: "weather?"},
			{Role: "assistant", ToolCalls: []provider.ToolCall{
				{ID: "call_1", Function: provider.FunctionCall{Name: "get_weather", Arguments: `{"loc":"SF"}`}},
			}},
		},
	}
	msgs, usage, err := loop.ResolveTools(context.Background(), req, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected messages")
	}
	if usage.TotalCalls() != 1 {
		t.Fatalf("expected 1 tool call, got %d", usage.TotalCalls())
	}
	if usage.Calls[0].ServerName != "weather-server" {
		t.Fatalf("expected server name, got %q", usage.Calls[0].ServerName)
	}
	if usage.Calls[0].CostUSD != 0.05 {
		t.Fatalf("expected cost 0.05, got %f", usage.Calls[0].CostUSD)
	}
	if usage.Calls[0].Result == nil || len(usage.Calls[0].Result.Content) == 0 {
		t.Fatal("expected tool result content")
	}
	// Tool messages should be appended to req.Messages before Chat.
	last := msgs[len(msgs)-1]
	if last.Role != "tool" || last.ToolCallID != "call_1" {
		t.Fatalf("expected tool message, got %+v", last)
	}
	// The provider should have been called exactly once (the final chat).
	if prov.idx != 1 {
		t.Fatalf("expected 1 chat call, got %d", prov.idx)
	}
}

func TestResolveToolsExceedsMaxIterations(t *testing.T) {
	tool := store.MCPTool{ID: 1, Name: "loop_tool", ServerID: 1}
	repo := &mockAgenticRepo{
		tools:  []store.MCPTool{tool},
		server: &store.MCPServer{Name: "loop-server"},
	}
	responses := make([]*provider.ChatResponse, MaxToolIterations+1)
	for i := range responses {
		responses[i] = &provider.ChatResponse{
			Choices: []provider.Choice{{
				Message: provider.Message{
					Role: "assistant",
					ToolCalls: []provider.ToolCall{
						{ID: "call_loop", Function: provider.FunctionCall{Name: "loop_tool", Arguments: "{}"}},
					},
				},
			}},
		}
	}
	prov := &mockProvider{responses: responses}
	mgr := NewClientManager(repo)
	loop := NewAgenticLoop(mgr, repo, prov)
	req := &provider.ChatRequest{
		Model: "test",
		Messages: []provider.Message{
			{Role: "user", Content: "loop"},
			{Role: "assistant", ToolCalls: []provider.ToolCall{
				{ID: "call_1", Function: provider.FunctionCall{Name: "loop_tool", Arguments: "{}"}},
			}},
		},
	}
	_, _, err := loop.ResolveTools(context.Background(), req, "", "")
	if err == nil {
		t.Fatal("expected error for exceeding max iterations")
	}
}