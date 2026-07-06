# Passthrough Evaluation — What Goes Straight Through vs Gets Re-marshalled

> Date: 2026-07 · Scope: `/v1/chat/completions` (non-streaming), the
> streaming variant, `/v1/models`, and the Admin API.
>
> Goal: catalogue every field on the wire between client ↔ gateway ↔
> upstream so we can decide which ones to forward verbatim, which
> to ignore, and which to translate.

## 1. Client → gateway (request body)

Chat completions request schema as observed today
(`internal/api/router.go:128`, `provider.ChatRequest`):

| Field | Status | Notes |
|---|---|---|
| `model` | passthrough | required; routed via L1–L5 |
| `messages[]` | passthrough | used only for `lastUserText()` (L4 intent) |
| `stream` | passthrough | triggers `streamChatCompletions`; non-stream returns JSON |
| `temperature` | **dropped** | not put on the wire to upstream |
| `top_p` | **dropped** | same |
| `frequency_penalty` / `presence_penalty` | **dropped** | same |
| `stop` | **dropped** | same |
| `n` | **dropped** | same |
| `max_tokens` / `max_completion_tokens` | **dropped** | same |
| `logit_bias` | **dropped** | same |
| `tools` / `tool_choice` | **dropped** | advertised in spec but no `provider` field carries it (see §4) |
| `response_format` (incl. `json_object`) | **dropped** | same |
| `seed` | **dropped** | same |
| `user` | **dropped** | same |
| `logprobs` / `top_logprobs` | **dropped** | same |
| `stream_options` | **dropped** | same |
| `parallel_tool_calls` | **dropped** | same |
| `metadata` | **dropped** | same |
| `store` | **dropped** | same |
| `reasoning_effort` | **dropped** | same |
| `prompt_cache_key` (Anthropic) | **dropped** | ❌ loses cheap prefix-cache |

`ChatRequest` struct only carries `Model`, `Messages`, `Stream` and
`Temperature/TopP/MaxTokens/Stop/N` are stored on the struct but **never
forwarded** to `provider.Chat(...)` (see `internal/provider/adapter.go`
for the upstream call shape).

### Streaming request body

Schema identical; the handler flips a flag. No additional fields.

## 2. Gateway → upstream (what we actually send)

`internal/provider/adapter.go:OpenAIProvider.Chat` JSON body:

```json
{
  "model": "<echo>",
  "messages": [...],
  "stream": <bool>
}
```

That is **all**. No temperature, no max_tokens, no tools, no
`response_format`. Every Chat Completions spec field except `model`,
`messages`, `stream` is silently dropped before going to the upstream
provider. Same applies for the Anthropic adapter (`multi.go`) and
Gemini adapter (translated `Contents` shape — but again, only the
message text and the system role are passed through).

### Why this matters

* `temperature=0.6` set by the client becomes provider default — the
  client gets deterministic answers it didn't ask for.
* `max_tokens=8` set by the client is ignored — the upstream will
  happily stream 16k tokens and the gateway has no way to cap it
  (other than our new `stream_max_body_bytes`).
* A user prompt cached by Anthropic on the upstream side (`prompt_cache_*`)
  needs `cache_control` blocks per message; we don't pass these, so
  every prompt-cache hit opportunity is lost.
* `tools` and `tool_choice` not forwarded → tool-calling clients
  silently get assistant answers with no tool execution.
* `response_format={type: json_object}` not forwarded → structured
  output promised to the caller but the upstream returns free text.

## 3. Upstream → gateway → client (response body)

### Non-streaming

| Field | Status | Notes |
|---|---|---|
| `id` | passthrough | provider-generated |
| `object` | passthrough | `chat.completion` |
| `created` | provider-set | OpenAI emits — kept |
| `model` | passthrough | echoes request |
| `choices[].index` | passthrough | |
| `choices[].message.role` | provider-set | |
| `choices[].message.content` | passthrough | |
| `choices[].finish_reason` | passthrough | |
| `choices[].logprobs` | passthrough | enabled by client but server log doesn't include it |
| `usage.prompt_tokens` | passthrough | |
| `usage.completion_tokens` | passthrough | |
| `usage.total_tokens` | passthrough | |
| `usage.prompt_tokens_details.cached_tokens` | **dropped** | cost benefit not propagated |
| `system_fingerprint` | **dropped** | provider version tag, minor |

### Streaming

Each chunk is JSON-encoded and wrapped in `data: {…}\n\n`. Same fields
above per chunk plus:

| Field | Status | Notes |
|---|---|---|
| `choices[].delta.role` | passthrough | |
| `choices[].delta.content` | passthrough | |
| `choices[].delta.function_call` | **lost** | function-call deltas broken |
| `choices[].delta.tool_calls` | **lost** | tool-calling responses get truncated |
| `choices[].finish_reason` | passthrough | mostly null per chunk |
| `usage` (in last chunk, OpenAI flag) | passthrough when present |

## 4. Admin client → gateway (request)

`internal/admin/handler.go` — every endpoint is fully typed and
specific fields are documented; nothing dropped intentionally.

## 5. Issues to fix

### 🔴 High-impact (provider contract)

1. **Forward the full `ChatRequest` to upstream** — change
   `provider.ChatRequest` to embed `json.RawMessage` of the original
   body (or add explicit fields) and re-emit it in the upstream call.
2. **Plumb `tools` / `tool_choice`** — required for Anthropic and
   OpenAI function calling. Bare minimum: add `Tools []Tool` and
   `ToolChoice any` to `ChatRequest`, forward verbatim.
3. **Forward `response_format`** — JSON-mode / structured-output clients
   break today.
4. **`prompt_cache_key` + `cache_control`** — required to take advantage
   of Anthropic's 90% cost reduction on repeated prefixes.
5. **Stream `delta.tool_calls` / `delta.function_call`** — currently
   swallowed inside `StreamChunk` (the field exists on the type but is
   never populated).

### 🟡 Medium

6. **`usage.prompt_tokens_details.cached_tokens` → cost discount**.
7. **`stream_options.include_usage`** — OpenAI returns usage once at end
   if requested; we accept it anyway but don't honour the request flag.
8. **`max_tokens` enforcement on gateway side** — currently only
   `stream_max_body_bytes` (a wire-side cap). Need upstream-emitted
   token cap.
9. **Preserve `seed` / `temperature` for reproducibility and determinism**.

### 🟢 Low

10. Forward `user`, `metadata`, `store`, `parallel_tool_calls`,
    `logit_bias`, etc., to support more esoteric cases.
11. Forward `system_fingerprint` for provider-side diagnostics.

## 6. Touch list for a "full passthrough" PR

| File | What changes |
|---|---|
| `internal/provider/types.go` | Add `Tools`, `ToolChoice`, `ResponseFormat`, `Stop`, `MaxTokens`, `Temperature`, `TopP`, `StreamOptions`, etc. |
| `internal/api/router.go` | Decode the full body into `ChatRequest`, forward verbatim. |
| `internal/provider/adapter.go` (OpenAI) | Use the full struct when POSTing. |
| `internal/provider/multi.go` (Anthropic, Gemini) | Translate; for Anthropic turn `messages[].cache_control` into `system` blocks; for Gemini express `generationConfig.responseSchema`. |
| `internal/model/usage.go` | Capture `prompt_tokens_details.cached_tokens`; deduct from `RealCostUSD`. |
| `web/src/api.ts` (frontend) | Surface new fields in the UI (`channel` page form). |

## 7. Recommended approach

Implement in two phases:

* **Phase A (passthrough fidelity)**: add the missing fields to
  `ChatRequest`; replace manual JSON construction in
  `OpenAIProvider.Chat` with `json.Marshal(req)`; ensure spec coverage
  for the three providers we already have. Add unit tests per field.
* **Phase B (cost optimisation)**: surface `cached_tokens` and
  `cache_control`, plumb them into L3 cost calculation.

Phase A unblocks tool-calling clients and structured-output clients.
Phase B unlocks the cheapest-of-cheap cache hits on Anthropic.
