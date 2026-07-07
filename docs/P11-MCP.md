# P11 — MCP Gateway

> Date: 2026-07 · Owner: llmRx maintainers · Status: design.
> Target: after P8-P10. Protocol still evolving — keep the
> implementation small and replaceable.

## 1. Why now

**Model Context Protocol** (MCP) is Anthropic's open protocol for
connecting LLMs to tools and data sources. As of mid-2025:

- Adopted by Cursor, Continue.dev, Cline, Zed, Claude Desktop,
  OpenAI's Agents SDK, and most new IDE clients.
- LiteLLM (v1.50+), Bifrost, and Kong AI Gateway have shipped MCP
  proxy support.
- Every LLM gateway that doesn't speak MCP will be invisible to
  the agentic ecosystem by end of 2026.

Without MCP, llmRx can route chat requests but cannot broker tool
calls. With MCP, llmRx becomes the **agentic gateway** — every
upstream LLM gets access to every tool the operator has registered.

## 2. What is MCP (in one paragraph)

MCP is JSON-RPC 2.0 over either HTTP+SSE (modern) or stdio. A
**server** exposes a list of **tools** (name + description + JSON
schema). A **client** connects, calls `tools/list`, then calls
`tools/call` with arguments. The server returns a result. The client
threads the tool results into the LLM context.

The transport we care about is **streamable HTTP**:
- `POST /mcp/{server-name}` — JSON-RPC request
- `GET /mcp/{server-name}` — SSE stream for server-initiated events
- Both share auth headers and a session ID.

## 3. Scope (P11 milestone)

| Feature | In scope | Out of scope |
|---|:---:|---|
| MCP **server** mode: expose llmRx's channels as tools (`channel_list`, `channel_call`) | ✅ | — |
| MCP **client** mode: register external MCP servers, route tool calls to them via LLM tool_use | ✅ | — |
| Auth forwarding (Bearer passthrough) | ✅ | — |
| Per-tool rate limit + spend tracking | ✅ | — |
| Web UI for MCP server registry | ✅ | — |
| stdio transport | parked | ✅ (P11.5; for sidecar MCP servers) |
| MCP resource / prompt primitives | parked | ✅ (P12; agents framework needs more than tools) |
| OAuth for MCP clients | parked | ✅ (P11.5; MCP 2025-06 spec) |

## 4. Two sides of MCP support

### 4.1 MCP **server** mode (llmRx exposes its capabilities)

Expose the existing channel pool as a tool. The LLM (running
elsewhere) sees:

```json
{
  "tools": [
    {
      "name": "channel_list",
      "description": "List configured llmRx channels and their models.",
      "inputSchema": { "type": "object", "properties": {} }
    },
    {
      "name": "channel_invoke",
      "description": "Invoke an LLM via one of llmRx's channels.",
      "inputSchema": {
        "type": "object",
        "properties": {
          "model": { "type": "string", "description": "Model name (e.g. gpt-4)" },
          "prompt": { "type": "string" },
          "system": { "type": "string" }
        },
        "required": ["model", "prompt"]
      }
    }
  ]
}
```

Wire up:
- `POST /mcp/llmrx` accepts JSON-RPC, dispatches to a small handler.
- `GET /mcp/llmrx` opens an SSE stream for server-initiated events.
- Auth via Bearer admin token.

This is essentially a thin RPC wrapper around the existing
`provider.Provider` + `pool.ChannelPool` machinery.

### 4.2 MCP **client** mode (llmRx consumes external MCP servers)

The more interesting side: register external MCP servers (GitHub,
Postgres, S3, custom in-house tools) and surface their tools via
the OpenAI `/v1/chat/completions` `tools` field. The LLM (e.g.
Claude) decides to call a tool, llmRx routes the tool call to the
right MCP server, gets the result, and feeds it back.

```
HTTP /v1/chat/completions (Claude via OpenAI protocol)
   ↓
llmRx sees tools=[github_create_issue, postgres_query, ...]
   ↓
LLM responds with tool_call(name="github_create_issue", args={...})
   ↓
llmRx dispatches via MCP client to the github MCP server
   ↓
MCP server returns tool_result
   ↓
llmRx re-invokes Claude with tool_call + tool_result
   ↓
... loop until LLM returns final answer
```

This is the **agentic loop** — and it's what makes llmRx a gateway
for agents, not just chat.

## 5. Storage

```sql
CREATE TABLE mcp_servers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,            -- "github", "postgres-prod"
    url  TEXT NOT NULL,                   -- https://mcp.example.com
    auth_header TEXT NOT NULL DEFAULT '', -- "Bearer xyz" or ""
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL
);

CREATE TABLE mcp_tools (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id INTEGER NOT NULL,
    name TEXT NOT NULL,                    -- "github_create_issue"
    description TEXT NOT NULL DEFAULT '',
    input_schema_json TEXT NOT NULL,      -- JSON Schema
    FOREIGN KEY (server_id) REFERENCES mcp_servers(id) ON DELETE CASCADE,
    UNIQUE (server_id, name)
);
```

Tools are **lazily fetched** from each MCP server via `tools/list`
on registration, then cached in `mcp_tools` for the gateway's
own `tools/list` response. Refresh every 5 min or on `/mcp-servers/{id}/refresh`.

## 6. Wire format

### 6.1 Server side — `POST /mcp/llmrx`

```jsonc
// Request
{ "jsonrpc": "2.0", "id": 1, "method": "tools/list" }

// Response
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": { "tools": [ ... ] }
}
```

```jsonc
// Request
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "tools/call",
  "params": {
    "name": "channel_invoke",
    "arguments": {
      "model": "gpt-4",
      "prompt": "summarise this"
    }
  }
}

// Response
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [{ "type": "text", "text": "..." }],
    "isError": false
  }
}
```

### 6.2 Client side — calling an external MCP server

```go
// internal/mcp/client.go
type Client struct {
    baseURL   string
    authHdr   string
    sessionID string  // assigned by server on initialize
    httpClient *http.Client
}

func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
    resp := c.rpc(ctx, "tools/list", nil)
    ...
}

func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*Result, error) {
    resp := c.rpc(ctx, "tools/call", map[string]any{
        "name": name,
        "arguments": args,
    })
    ...
}
```

Each MCP server gets its own `Client` instance; one `Client` per `mcp_servers` row.

## 7. Agentic loop integration

The `tools` field on the OpenAI request body already flows through
(Passthrough A). The new piece is detecting `tool_calls` in the
upstream's response and dispatching them.

```go
// internal/api/chat.go (NEW: tool dispatcher middleware)
func (h *Handler) ChatCompletions(w, r) {
    // ... existing routing ...

    resp, err := prov.Chat(&req, ...)
    if err != nil { ... }

    // NEW: if response has tool_calls and the names are MCP tools,
    //      call them, build tool_results, re-invoke Chat.
    for len(resp.Choices[0].Message.ToolCalls) > 0 {
        results := []provider.Message{}
        for _, tc := range resp.Choices[0].Message.ToolCalls {
            result, err := h.mcp.CallTool(ctx, tc.Function.Name, parseArgs(tc.Function.Arguments))
            if err != nil {
                results = append(results, provider.Message{
                    Role: "tool",
                    ToolCallID: tc.ID,
                    Content: fmt.Sprintf("error: %v", err),
                })
                continue
            }
            results = append(results, provider.Message{
                Role: "tool",
                ToolCallID: tc.ID,
                Content: result.Text,
            })
        }

        // Append tool results to messages; re-invoke Chat.
        req.Messages = append(req.Messages, resp.Choices[0].Message, results...)
        resp, err = prov.Chat(&req, ...)
        // Loop guard: max_iterations = 10
    }
    writeJSON(w, resp)
}
```

Loop guard: max 10 iterations. After that, return the response with
an error frame.

## 8. Streaming + tool calls

Streaming tool-call loops are tricky because the upstream emits
`delta.tool_calls` incrementally. Implementation strategy:

* Accumulate `delta.tool_calls` across chunks until `finish_reason` is
  set (today we don't track this; P11 also fixes it).
* When the final chunk has tool_calls, run the agentic loop
  **outside** the streaming response — issue a non-streaming Chat
  with the accumulated messages and stream the final response.
* Return a single SSE event `event: agentic-loop` to indicate the
  switch.

Alternative: refuse streaming + tool calls together; document that
"set stream=false when using tools".

## 9. Per-tool rate limit + spend

Each MCP tool call counts as one log row with `endpoint='mcp'`:

```go
type Log struct {
    // ... existing fields ...
    Endpoint  string  `json:"endpoint"` // 'mcp'
    Units     int     `json:"units"`     // 1 per tool call
    Model     string  `json:"model"`     // MCP server name (e.g. "github")
}
```

The token cache's TPM counter should NOT be bumped for MCP calls
(they don't cost LLM tokens). The RPM counter should be bumped
once per call.

`IncrementTokenSpend` is gated by a per-tool rate card:
```sql
CREATE TABLE mcp_tool_pricing (
    mcp_tool_id INTEGER PRIMARY KEY,
    price_per_call_usd REAL NOT NULL DEFAULT 0,
    FOREIGN KEY (mcp_tool_id) REFERENCES mcp_tools(id) ON DELETE CASCADE
);
```

Default 0 (free). Tools explicitly priced via admin UI.

## 10. Web UI

* **Settings → MCP Servers** — list / create / delete / refresh
* **Settings → MCP Tools** — per-tool pricing
* **Dashboard card** — MCP calls in last 24h, total MCP spend

## 11. Tests

* **JSON-RPC parsing**: standard `tools/list` / `tools/call`
* **MCP client transport**: mock MCP server in test (httptest),
  assert request shape
* **Tool dispatch loop**: 2-call loop produces final answer; 11-call
  loop terminates with error
* **Loop guard**: `max_iterations=10` enforced
* **Per-tool spend**: token spend increments once per tool call
* **Auth**: missing Bearer → 401; wrong Bearer → 403
* **MCP server registry**: CRUD round-trips correctly

## 12. Acceptance criteria

| Metric | Target |
|---|---|
| `POST /mcp/llmrx` answers JSON-RPC | ✅ |
| External MCP server tools surface on `/v1/chat/completions` | ✅ |
| Agentic loop terminates within 10 iterations | ✅ |
| Per-tool spend tracked | ✅ |
| Coverage ≥ 70 % for `internal/mcp` | ✅ |
| Coverage overall ≥ 65 % | ✅ |

## 13. Files to add / touch

```
internal/mcp/protocol.go        # JSON-RPC 2.0 types (Request / Response / Error)
internal/mcp/server.go          # MCP server (gateways' own tools)
internal/mcp/client.go          # MCP client (talks to external servers)
internal/mcp/registry.go        # mcp_servers + mcp_tools CRUD
internal/mcp/agentic.go         # tool dispatch loop
internal/api/router.go          # wire agentic loop into ChatCompletions
internal/api/router.go          # SSE event for "agentic-loop" transition
internal/store/sqlite.go        # mcp_servers + mcp_tools + mcp_tool_pricing tables
internal/store/store.go         # CRUD signatures
internal/admin/handler.go       # MCP CRUD endpoints
internal/model/types.go         # Log.Endpoint += 'mcp'
web/src/pages/Settings.tsx      # MCP Servers tab
internal/mcp/protocol_test.go
internal/mcp/client_test.go
internal/mcp/agentic_test.go
internal/mcp/registry_test.go
```

## 14. New dependencies

* `github.com/google/uuid` — for session IDs (already common)
* **No MCP SDK** — the protocol is small enough (JSON-RPC over HTTP+SSE)
  to implement in ~500 LoC; adopting an SDK would couple us to one
  vendor's pace. We can re-evaluate in P11.5.

## 15. Rollout

1. Land `internal/mcp/protocol.go` + `client.go` + `server.go`.
2. Land `internal/mcp/registry.go` + admin endpoints + UI.
3. Land agentic loop integration (single-shot, no streaming).
4. Land streaming-event bridge for tool-call loops.
5. Land per-tool spend tracking.
6. README + CHANGELOG.
7. Optional: stdio transport (P11.5).

## 16. Risks

| Risk | Mitigation |
|---|---|
| MCP spec changes mid-implementation | Keep impl small; version-pin JSON-RPC error codes |
| Agentic loop runs forever | Hard cap 10 iterations; surface as error frame |
| MCP server returns huge tool result | Truncate at 100 KB; flag in log |
| OAuth flow needed for some servers | Park to P11.5; basic bearer is enough for v1 |
| Streaming + tool calls interaction | Document restriction; v1 supports non-streaming tools only |