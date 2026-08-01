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

func (a *AgenticLoop) Execute(ctx context.Context, req *provider.ChatRequest, routeKey, baseURL string) (*provider.ChatResponse, error) {
	allTools, err := a.repo.GetAllMCPTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("mcp: load tools: %w", err)
	}
	mcpToolNames := make(map[string]int64, len(allTools))
	for _, t := range allTools {
		mcpToolNames[t.Name] = t.ServerID
	}
	if len(mcpToolNames) == 0 {
		resp, _, err := a.provider.Chat(ctx, req, routeKey, baseURL)
		return resp, err
	}
	for iter := 0; iter < MaxToolIterations; iter++ {
		resp, statusCode, err := a.provider.Chat(ctx, req, routeKey, baseURL)
		if err != nil {
			return nil, fmt.Errorf("mcp: chat error (status %d): %w", statusCode, err)
		}
		if len(resp.Choices) == 0 {
			return resp, nil
		}
		msg := resp.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			return resp, nil
		}
		executions := a.dispatchToolCalls(ctx, msg.ToolCalls, mcpToolNames)
		toolMessages := make([]provider.Message, 0, len(executions))
		for _, ex := range executions {
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
	return nil, fmt.Errorf("agentic loop exceeded %d iterations", MaxToolIterations)
}

func (a *AgenticLoop) dispatchToolCalls(ctx context.Context, calls []provider.ToolCall, mcpToolNames map[string]int64) []ToolExecution {
	executions := make([]ToolExecution, 0, len(calls))
	for _, tc := range calls {
		serverID, ok := mcpToolNames[tc.Function.Name]
		if !ok {
			executions = append(executions, ToolExecution{
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Err:        fmt.Errorf("unknown tool: %s", tc.Function.Name),
			})
			continue
		}
		client, err := a.mgr.GetClient(ctx, serverID)
		if err != nil || client == nil {
			executions = append(executions, ToolExecution{
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Err:        fmt.Errorf("mcp client unavailable: %v", err),
			})
			continue
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			executions = append(executions, ToolExecution{
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Err:        fmt.Errorf("invalid args: %v", err),
			})
			continue
		}
		result, callErr := client.CallTool(ctx, tc.Function.Name, args)
		if callErr != nil {
			executions = append(executions, ToolExecution{
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Err:        callErr,
			})
			continue
		}
		executions = append(executions, ToolExecution{
			ToolCallID: tc.ID,
			Name:       tc.Function.Name,
			Result:     result,
		})
	}
	return executions
}

func (a *AgenticLoop) HasTools(ctx context.Context) (bool, error) {
	tools, err := a.repo.GetAllMCPTools(ctx)
	if err != nil {
		return false, err
	}
	return len(tools) > 0, nil
}