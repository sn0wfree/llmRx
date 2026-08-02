package mcp

import (
	"context"
	"encoding/json"
	"sync"
)

type ToolHandler func(ctx context.Context, name string, args map[string]any) (map[string]any, error)

type Server struct {
	mu      sync.RWMutex
	tools   []Tool
	handler ToolHandler
}

func NewServer(tools []Tool, handler ToolHandler) *Server {
	return &Server{
		tools:   tools,
		handler: handler,
	}
}

func DefaultTools() []Tool {
	return []Tool{
		{
			Name:        "channel_list",
			Description: "List configured llmRx channels and their models.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "channel_invoke",
			Description: "Invoke an LLM via one of llmRx's channels.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"model": map[string]any{
						"type":        "string",
						"description": "Model name (e.g. gpt-4)",
					},
					"prompt": map[string]any{
						"type":        "string",
						"description": "User prompt",
					},
					"system": map[string]any{
						"type":        "string",
						"description": "System prompt (optional)",
					},
				},
				"required": []any{"model", "prompt"},
			},
		},
	}
}

func (s *Server) Tools() []Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tools
}

func (s *Server) HandleRequest(ctx context.Context, req *Request) *Response {
	switch req.Method {
	case "tools/list":
		return s.handleListTools(ctx, req)
	case "tools/call":
		return s.handleCallTool(ctx, req)
	default:
		return NewError(req.ID, CodeMethodNotFound, "method not found: "+req.Method)
	}
}

func (s *Server) handleListTools(_ context.Context, req *Request) *Response {
	s.mu.RLock()
	tools := s.tools
	s.mu.RUnlock()
	return NewResponse(req.ID, map[string]any{"tools": tools})
}

func (s *Server) handleCallTool(ctx context.Context, req *Request) *Response {
	name, _ := req.Params["name"].(string)
	args, _ := req.Params["arguments"].(map[string]any)
	if s.handler == nil {
		return NewError(req.ID, CodeInternalError, "no tool handler configured")
	}
	result, err := s.handler(ctx, name, args)
	if err != nil {
		return NewError(req.ID, CodeInternalError, err.Error())
	}
	return NewResponse(req.ID, result)
}

func (s *Server) HandleHTTP(ctx context.Context, body []byte) *Response {
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return NewError(nil, CodeParseError, "invalid JSON: "+err.Error())
	}
	if req.JSONRPC != Version {
		return NewError(req.ID, CodeInvalidRequest, "invalid jsonrpc version")
	}
	if req.Method == "" {
		return NewError(req.ID, CodeInvalidRequest, "method is required")
	}
	return s.HandleRequest(ctx, &req)
}

func (s *Server) Close() {}

func MarshalToolResult(text string, isError bool) map[string]any {
	result := &ToolResult{
		Content: []ContentItem{{Type: "text", Text: text}},
		IsError: isError,
	}
	b, _ := json.Marshal(result)
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	return raw
}
