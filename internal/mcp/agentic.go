package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/store"
)

const MaxToolIterations = 10

// MCPCall records one executed MCP tool call with its billed cost
// and the tool result/error needed to build the follow-up tool
// message.
type MCPCall struct {
	ToolCallID string
	Name       string
	ServerName string
	Result     *ToolResult
	Err        error
	CostUSD    float64
}

// ToolName returns the tool's name.
func (c MCPCall) ToolName() string { return c.Name }

// MCPUsage aggregates every tool call executed during one agentic
// loop run. Callers use it to emit per-call spend logs.
type MCPUsage struct {
	Calls []MCPCall
}

// TotalCalls returns the number of executed tool calls.
func (u MCPUsage) TotalCalls() int { return len(u.Calls) }

// TotalCostUSD returns the summed billed cost across all calls.
func (u MCPUsage) TotalCostUSD() float64 {
	var total float64
	for _, c := range u.Calls {
		total += c.CostUSD
	}
	return total
}

// ServerNames returns the unique MCP server names involved, in
// first-seen order.
func (u MCPUsage) ServerNames() []string {
	seen := make(map[string]bool)
	var out []string
	for _, c := range u.Calls {
		if c.ServerName != "" && !seen[c.ServerName] {
			seen[c.ServerName] = true
			out = append(out, c.ServerName)
		}
	}
	return out
}

type ToolExecution struct {
	ToolCallID string
	Name       string
	Result     *ToolResult
	Err        error
}

type AgenticLoop struct {
	mgr      *ClientManager
	repo     store.MCPRepository
	provider provider.Provider
}

func NewAgenticLoop(mgr *ClientManager, repo store.MCPRepository, prov provider.Provider) *AgenticLoop {
	return &AgenticLoop{mgr: mgr, repo: repo, provider: prov}
}

// Execute runs the agentic tool-dispatch loop. It returns the final
// provider response plus a MCPUsage summary of every tool call made,
// so the caller can emit per-call spend logs.
func (a *AgenticLoop) Execute(ctx context.Context, req *provider.ChatRequest, routeKey, baseURL string) (*provider.ChatResponse, MCPUsage, error) {
	var usage MCPUsage
	allTools, err := a.repo.GetAllMCPTools(ctx)
	if err != nil {
		return nil, usage, fmt.Errorf("mcp: load tools: %w", err)
	}
	toolsByName := make(map[string]store.MCPTool, len(allTools))
	for _, t := range allTools {
		toolsByName[t.Name] = t
	}
	if len(toolsByName) == 0 {
		resp, _, err := a.provider.Chat(ctx, req, routeKey, baseURL)
		return resp, usage, err
	}
	for iter := 0; iter < MaxToolIterations; iter++ {
		resp, statusCode, err := a.provider.Chat(ctx, req, routeKey, baseURL)
		if err != nil {
			return nil, usage, fmt.Errorf("mcp: chat error (status %d): %w", statusCode, err)
		}
		if len(resp.Choices) == 0 {
			return resp, usage, nil
		}
		msg := resp.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			return resp, usage, nil
		}
		calls := a.dispatchToolCalls(ctx, msg.ToolCalls, toolsByName)
		usage.Calls = append(usage.Calls, calls...)
		toolMessages := make([]provider.Message, 0, len(calls))
		for _, ex := range calls {
			text := ""
			if ex.Result != nil {
				var parts []string
				for _, c := range ex.Result.Content {
					parts = append(parts, c.Text)
				}
				text = strings.Join(parts, "\n")
			} else if ex.Err != nil {
				text = fmt.Sprintf("error: %v", ex.Err)
			}
			toolMessages = append(toolMessages, provider.Message{
				Role:       "tool",
				ToolCallID: ex.ToolCallID,
				Content:    text,
			})
		}
		req.Messages = append(append(req.Messages, msg), toolMessages...)
	}
	logging.Warn("mcp: agentic loop exceeded max iterations",
		logging.F("max_iterations", MaxToolIterations))
	return nil, usage, fmt.Errorf("agentic loop exceeded %d iterations", MaxToolIterations)
}

// dispatchToolCalls executes each requested tool call and returns the
// per-call executions (including cost) in request order.
func (a *AgenticLoop) dispatchToolCalls(ctx context.Context, calls []provider.ToolCall, toolsByName map[string]store.MCPTool) []MCPCall {
	executions := make([]MCPCall, 0, len(calls))
	for _, tc := range calls {
		rec := MCPCall{Name: tc.Function.Name}
		tool, ok := toolsByName[tc.Function.Name]
		if !ok {
			executions = append(executions, MCPCall{
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Err:        fmt.Errorf("unknown tool: %s", tc.Function.Name),
			})
			continue
		}
		rec.ServerName = a.serverName(ctx, tool.ServerID)
		client, err := a.mgr.GetClient(ctx, tool.ServerID)
		if err != nil || client == nil {
			executions = append(executions, MCPCall{
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Err:        fmt.Errorf("mcp client unavailable: %v", err),
			})
			continue
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			executions = append(executions, MCPCall{
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Err:        fmt.Errorf("invalid args: %v", err),
			})
			continue
		}
		result, callErr := client.CallTool(ctx, tc.Function.Name, args)
		if callErr != nil {
			executions = append(executions, MCPCall{
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Err:        callErr,
			})
			continue
		}
		rec.ToolCallID = tc.ID
		rec.Result = result
		rec.CostUSD = a.priceFor(ctx, tool.ID)
		executions = append(executions, rec)
	}
	return executions
}

// serverName resolves an MCP server's display name for logging.
// Unresolvable servers fall back to an empty name (the router will
// use the tool name).
func (a *AgenticLoop) serverName(ctx context.Context, serverID int64) string {
	if a.repo == nil || serverID <= 0 {
		return ""
	}
	srv, err := a.repo.GetMCPServer(ctx, serverID)
	if err != nil || srv == nil {
		return ""
	}
	return srv.Name
}

// priceFor looks up the per-call price of a tool. Missing pricing
// rows or store errors default to 0 (free), matching the rate card
// semantics (default price_per_call_usd = 0).
func (a *AgenticLoop) priceFor(ctx context.Context, toolID int64) float64 {
	if a.repo == nil || toolID <= 0 {
		return 0
	}
	p, err := a.repo.GetMCPToolPricing(ctx, toolID)
	if err != nil || p == nil {
		return 0
	}
	return p.PricePerCallUSD
}

func (a *AgenticLoop) HasTools(ctx context.Context) (bool, error) {
	tools, err := a.repo.GetAllMCPTools(ctx)
	if err != nil {
		return false, err
	}
	return len(tools) > 0, nil
}

// ResolveTools is like Execute but starts from an existing assistant
// message with tool_calls in req.Messages (e.g. from a streaming
// response). It dispatches tools and calls Chat (non-streaming) in a
// loop until no more tool_calls remain, then returns the accumulated
// Messages so the caller can issue a final StreamChat. The first
// tool_calls dispatch is done from the last assistant message in
// req.Messages. req.Messages is modified in place.
func (a *AgenticLoop) ResolveTools(ctx context.Context, req *provider.ChatRequest, routeKey, baseURL string) ([]provider.Message, MCPUsage, error) {
	var usage MCPUsage
	allTools, err := a.repo.GetAllMCPTools(ctx)
	if err != nil {
		return nil, usage, fmt.Errorf("mcp: load tools: %w", err)
	}
	toolsByName := make(map[string]store.MCPTool, len(allTools))
	for _, t := range allTools {
		toolsByName[t.Name] = t
	}
	if len(toolsByName) == 0 {
		return req.Messages, usage, nil
	}
	for iter := 0; iter < MaxToolIterations; iter++ {
		var lastToolCalls []provider.ToolCall
		if len(req.Messages) > 0 {
			last := req.Messages[len(req.Messages)-1]
			if last.Role == "assistant" && len(last.ToolCalls) > 0 {
				lastToolCalls = last.ToolCalls
			}
		}
		if len(lastToolCalls) == 0 {
			return req.Messages, usage, nil
		}
		calls := a.dispatchToolCalls(ctx, lastToolCalls, toolsByName)
		usage.Calls = append(usage.Calls, calls...)
		toolMessages := make([]provider.Message, 0, len(calls))
		for _, ex := range calls {
			text := ""
			if ex.Result != nil {
				var parts []string
				for _, c := range ex.Result.Content {
					parts = append(parts, c.Text)
				}
				text = strings.Join(parts, "\n")
			} else if ex.Err != nil {
				text = fmt.Sprintf("error: %v", ex.Err)
			}
			toolMessages = append(toolMessages, provider.Message{
				Role:       "tool",
				ToolCallID: ex.ToolCallID,
				Content:    text,
			})
		}
		req.Messages = append(req.Messages, toolMessages...)
		resp, statusCode, err := a.provider.Chat(ctx, req, routeKey, baseURL)
		if err != nil {
			return nil, usage, fmt.Errorf("mcp: chat error (status %d): %w", statusCode, err)
		}
		if len(resp.Choices) == 0 {
			return req.Messages, usage, nil
		}
		msg := resp.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			return req.Messages, usage, nil
		}
		req.Messages = append(req.Messages, msg)
	}
	logging.Warn("mcp: agentic loop exceeded max iterations in ResolveTools",
		logging.F("max_iterations", MaxToolIterations))
	return nil, usage, fmt.Errorf("agentic loop exceeded %d iterations", MaxToolIterations)
}
