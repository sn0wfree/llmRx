# P9 — Multimodal Endpoints (Image / Audio / Rerank / Embeddings)

> Date: 2026-07 · Owner: llmRx maintainers · Status: revised after LiteLLM + opencode research.
> Target: after P8 deferred; precedes P10.

## 1. Why now

OpenAI-compatible SDK clients (Cursor / Continue / Open WebUI /
LibreChat / ChatGPT-Next-Web) ship a fixed catalogue of endpoints:

```
POST /v1/chat/completions      text + tool calls         ✅ P7+
POST /v1/embeddings            vector embeddings          ❌ NEW
POST /v1/images/generations    DALL-E / Imagen / Flux     ❌ NEW
POST /v1/images/edits          image + mask + prompt      ❌ NEW
POST /v1/images/variations     source-image variations    ❌ NEW (lite)
POST /v1/audio/transcriptions  Whisper STT                ❌ NEW
POST /v1/audio/translations    STT + translate            ❌ NEW (lite)
POST /v1/audio/speech          TTS                        ❌ NEW
POST /v1/rerank                Cohere / Jina / BGE        ❌ NEW
POST /v1/moderations           OpenAI moderation          parked (P12)
POST /v1/videos                Sora 2                     parked (P9.5)
POST /v1/responses             OpenAI Responses API       parked (P12)
```

When a customer installs llmRx and calls `/v1/images/generations`,
today they get **404**. They uninstall. The same client works
against LiteLLM / One-API / Bifrost out of the box.

**Research findings (LiteLLM source + opencode source)** confirm:

1. **LiteLLM pattern** — every non-chat endpoint is a *thin handler*
   wrapping `ProxyBaseLLMRequestProcessing.base_process_llm_request()`
   with a `route_type` enum (`aimage_generation`, `arerank`,
   `atranscription` …). Shared infrastructure (auth, logging, cost,
   guardrails, OTel, fallbacks, load balancing) flows automatically.
2. **opencode pattern** — declares per-model `input/output modalities`
   (`{text, audio, image, video, pdf}`) and **gracefully degrades**
   unsupported parts to error text instead of 4xx.
3. **Industry wire format** — OpenAI is canonical. Cohere v2 schema
   is canonical for `/v1/rerank`. Multipart form for image/audio
   uploads; JSON for everything else.

This document revises the original spec to adopt these patterns.

## 2. Scope (P9 milestone)

| Endpoint                              | In scope | Notes                                       |
|---------------------------------------|:---:|---------------------------------------------|
| `/v1/embeddings`                      | ✅ | OpenAI / Cohere / Voyage / Gemini            |
| `/v1/images/generations`              | ✅ | OpenAI (DALL-E 3, gpt-image-1)               |
| `/v1/images/edits`                    | ✅ | OpenAI + Gemini (multipart)                  |
| `/v1/images/variations`               | ✅ | OpenAI (no prompt)                           |
| `/v1/audio/transcriptions`            | ✅ | OpenAI Whisper + Groq + Deepgram             |
| `/v1/audio/translations`              | ✅ | Whisper translate                            |
| `/v1/audio/speech`                    | ✅ | OpenAI TTS + ElevenLabs                      |
| `/v1/rerank`                          | ✅ | Cohere / Jina / Voyage / BGE                 |
| `/v1/videos`, `/v1/videos/{id}/*`     | parked | P9.5 (Sora 2 API still volatile)            |
| `/v1/moderations`                     | parked | P12 (low demand)                             |
| `/v1/responses` (OpenAI Responses API) | parked | P12 (GPT-5 native, large surface)            |
| stdio MCP transport                   | parked | P11.5                                        |

## 3. Two design decisions

### 3.1 Channel endpoint declaration (vs. implicit Protocol)

Original P9 design inferred endpoint from `channel.Protocol`. New
design uses an explicit `channel.Endpoint` field, matching LiteLLM's
`model_info.mode`:

```sql
ALTER TABLE channels ADD COLUMN endpoint TEXT NOT NULL DEFAULT 'chat';
-- 'chat' | 'image_generation' | 'image_edit' | 'audio_transcription'
-- | 'audio_speech' | 'embedding' | 'rerank' | 'video_generation'

ALTER TABLE channels ADD COLUMN capabilities TEXT NOT NULL DEFAULT '{}';
-- JSON: {"input":{"text":1,"image":1,"audio":0,"video":0,"pdf":0},
--         "output":{"text":1,"image":1,"audio":1,"video":0,"pdf":0},
--         "tool_call":1,"reasoning":0,"attachment":1}
-- (mirrors opencode ProviderCapabilities schema)
```

**Why split?** A single OpenAI key handles chat + image + audio + embeddings.
The endpoint field routes by URL path; the capabilities field validates
the model's modality support (graceful degradation).

### 3.2 Capability check: error text, not 4xx

opencode pattern — when a model doesn't support a modality, the
client replaces the part with an `ERROR: ...` text rather than
failing the request:

```ts
// opencode/provider/transform.ts
if (!model.capabilities.input[modality]) {
  return { type: "text", text: `ERROR: Cannot read ${name} (this model does not support ${modality} input). Inform the user.` }
}
```

We adopt this for llmRx:
- **Inbound multimodal chat**: if a model doesn't support the part's
  modality, replace with `ERROR: ...` text. Continue serving.
- **Endpoint routing**: if no channel has the required endpoint,
  return 503 (not 404) — "no channel can handle this endpoint".

## 4. Storage

```sql
-- 4.1 Channels: new columns (see 3.1)
ALTER TABLE channels ADD COLUMN endpoint TEXT NOT NULL DEFAULT 'chat';
ALTER TABLE channels ADD COLUMN capabilities TEXT NOT NULL DEFAULT '{}';

-- 4.2 Endpoint pricing: per-unit (not per-token)
CREATE TABLE endpoint_prices (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id   INTEGER NOT NULL,
    endpoint     TEXT    NOT NULL,        -- 'image_generation' | 'audio_speech' | ...
    model        TEXT    NOT NULL,        -- 'gpt-image-1' | 'tts-1' | 'whisper-1' | ...
    unit         TEXT    NOT NULL,        -- 'per_image' | 'per_second' | 'per_char'
    unit_size    REAL    NOT NULL DEFAULT 1,  -- 1024x1024=1, 1792x1024=2 (image); 1.0 (default)
    price_usd    REAL    NOT NULL DEFAULT 0,
    FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE,
    UNIQUE (channel_id, model, unit, unit_size)
);

-- 4.3 Endpoint audit log rows
-- (logs.endpoint already exists in P6 schema; expand enum to include new endpoints)
```

**Pricing examples**:

| Channel.Model | Endpoint | Unit | Unit size | Price |
|---|---|---|---|---|
| OpenAI gpt-image-1 | image_generation | per_image | 1024x1024 | $0.020 |
| OpenAI gpt-image-1 | image_generation | per_image | 1536x1024 | $0.030 |
| OpenAI dall-e-3 | image_generation | per_image | 1024x1024 | $0.040 |
| OpenAI tts-1 | audio_speech | per_char | 1 | $0.000015 |
| OpenAI whisper-1 | audio_transcription | per_second | 1 | $0.006 |
| Cohere rerank-3.5 | rerank | per_search_unit | 1 | $0.002 |

## 5. Provider interface extension

```go
// internal/provider/provider.go
type Provider interface {
    // existing (P7+)
    Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
    StreamChat(ctx context.Context, req *ChatRequest) (<-chan Chunk, error)

    // new (P9)
    Embed(ctx context.Context, req *EmbedRequest) (*EmbedResponse, error)
    GenerateImage(ctx context.Context, req *ImageRequest) (*ImageResponse, error)
    EditImage(ctx context.Context, req *ImageEditRequest) (*ImageResponse, error)
    Transcribe(ctx context.Context, req *TranscribeRequest) (*TranscriptionResponse, error)
    Speech(ctx context.Context, req *TTSRequest) (io.ReadCloser, error)
    Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error)

    // meta
    Capabilities() Capabilities
}
```

Each provider implements only the methods it supports. For unsupported
methods, return `ErrUnsupported`.

```go
// internal/provider/openai/multi.go
func (p *Provider) Embed(ctx, req) (*EmbedResponse, error) { ... }
func (p *Provider) GenerateImage(ctx, req) (*ImageResponse, error) { ... }
func (p *Provider) EditImage(ctx, req) (*ImageResponse, error) { ... }
func (p *Provider) Transcribe(ctx, req) (*TranscriptionResponse, error) { ... }
func (p *Provider) Speech(ctx, req) (io.ReadCloser, error) { ... }
// Rerank: NOT supported (returns ErrUnsupported)
```

## 6. Handler layout

```
internal/api/
├── chat.go              (existing, P7+)
├── embeddings.go        NEW (250 LoC, 1 endpoint)
├── images.go            NEW (350 LoC, 3 endpoints)
├── audio.go             NEW (400 LoC, 3 endpoints)
├── rerank.go            NEW (250 LoC, 1 endpoint)
└── multimodal.go        NEW (200 LoC, shared: hooks, headers, cost)

internal/multimodal/
├── multipart.go         NEW (parse multipart → bytes + meta)
├── capabilities.go      NEW (modality validation + graceful degradation)
├── cost.go              NEW (per-unit pricing + token hybrid)
└── response.go          NEW (hidden params headers, error mapping)
```

### 6.1 Shared base processor pattern

Following LiteLLM's thin-handler discipline, every endpoint
handler is ~30-60 LoC and delegates to a shared base processor:

```go
// internal/api/multimodal.go
func (h *Handler) processMultimodal(
    w http.ResponseWriter,
    r *http.Request,
    endpoint string,                  // 'image_generation', 'rerank', ...
    parsed *multimodal.Parsed,         // JSON or multipart
) {
    // 1. token auth + rate limit
    info := middleware.AuthFromContext(r.Context())
    if !h.limiter.Allow(info.TokenID, estimateCost(parsed, endpoint)) {
        writeError(w, 429, "rate_limit_exceeded"); return
    }

    // 2. route to channel
    ch := h.router.PickByEndpoint(endpoint)
    if ch == nil {
        writeError(w, 503, "no_channel_available"); return
    }

    // 3. capability check
    if err := multimodal.CheckCapability(ch, endpoint, parsed); err != nil {
        writeError(w, 400, err.Error()); return
    }

    // 4. pre-call hook (guardrails)
    h.hooks.PreCall(r.Context(), endpoint, parsed)

    // 5. upstream call
    start := time.Now()
    resp, err := h.provider.For(ch).Call(r.Context(), endpoint, parsed)
    latency := time.Since(start)

    // 6. post-call hook (OTel, logging, cost)
    h.hooks.PostCall(r.Context(), endpoint, ch, resp, err, latency)

    // 7. add hidden params headers (LiteLLM pattern)
    multimodal.WriteHiddenHeaders(w, ch, resp, err)

    // 8. write response
    if err != nil {
        writeUpstreamError(w, err); return
    }
    resp.WriteTo(w)
}
```

### 6.2 Multipart parsing

```go
// internal/multimodal/multipart.go
type Parsed struct {
    Fields     map[string]string    // prompt, model, response_format, ...
    Files      map[string][]byte    // image[0], mask, file
    FileNames  map[string]string    // image[0].png → MIME inferred
    FileMetas  map[string]multipart.FileHeader
}

func Parse(r *http.Request, maxMemory int64) (*Parsed, error) {
    // Auto-detect content-type
    ct := r.Header.Get("Content-Type")
    if strings.HasPrefix(ct, "multipart/form-data") {
        return parseMultipart(r, maxMemory)
    }
    return parseJSON(r)
}

// filenames used by providers' SDKs to infer MIME
func (p *Parsed) FileNameFor(key string) string {
    if name, ok := p.FileNames[key]; ok { return name }
    return key + ".bin"  // default
}
```

### 6.3 Capability check (graceful degradation)

```go
// internal/multimodal/capabilities.go
func CheckAndDegrade(
    ch *model.Channel,
    endpoint string,
    parsed *Parsed,
) {
    // For chat endpoint, degrade multimodal input parts
    if endpoint == "chat" && parsed.Fields["messages"] != "" {
        degradeUnsupportedParts(ch, parsed)
        return
    }
    // For other endpoints, validate the *single* required modality
    req := requiredModality(endpoint)
    if req == "" { return }
    if !supports(ch.Capabilities(), req) {
        // Replace content with error text
        parsed.Fields["prompt"] = "ERROR: channel " + ch.Name + " does not support " + req
    }
}

func requiredModality(endpoint string) string {
    switch endpoint {
    case "image_generation":  return "output.image"
    case "image_edit":        return "input.image,output.image"
    case "audio_speech":      return "output.audio"
    case "audio_transcription": return "input.audio"
    case "rerank":            return ""  // text only
    case "embedding":         return ""  // text or image
    default:                  return ""
    }
}

func supports(caps Capabilities, mod string) bool {
    parts := strings.Split(mod, ",")
    for _, p := range parts {
        side, mod := splitModality(p)  // "input.image" → "input", "image"
        if !caps[side][mod] { return false }
    }
    return true
}

func mimeToModality(mime string) string {
    switch {
    case strings.HasPrefix(mime, "image/"): return "image"
    case strings.HasPrefix(mime, "audio/"): return "audio"
    case strings.HasPrefix(mime, "video/"): return "video"
    case mime == "application/pdf":         return "pdf"
    }
    return ""
}
```

### 6.4 Cost calculation (per-unit hybrid)

```go
// internal/multimodal/cost.go
type CostBreakdown struct {
    InputUSD  float64
    OutputUSD float64
    UnitUSD   float64
    TotalUSD  float64
}

func CalcCost(ch *model.Channel, endpoint string, req *Parsed, resp *Response) CostBreakdown {
    var b CostBreakdown
    switch endpoint {
    case "chat":
        // existing token-based math
        promptCost := float64(resp.Usage.PromptTokens) * ch.InputPriceUSD / 1_000_000
        compCost   := float64(resp.Usage.CompletionTokens) * ch.OutputPriceUSD / 1_000_000
        b.InputUSD  = promptCost
        b.OutputUSD = compCost

    case "image_generation":
        // per-image pricing (vary by size)
        unitSize := req.SizeUnit()  // 1024x1024 → 1, 1792x1024 → 2
        price := lookupPrice(ch.ID, endpoint, req.Model, "per_image", unitSize)
        b.UnitUSD = price * float64(resp.N)

    case "audio_speech":
        charCount := utf8.RuneCountInString(req.Fields["input"])
        price := lookupPrice(ch.ID, endpoint, req.Model, "per_char", 1)
        b.UnitUSD = price * float64(charCount)

    case "audio_transcription":
        seconds := req.DurationSeconds  // from upstream or estimate
        price := lookupPrice(ch.ID, endpoint, req.Model, "per_second", 1)
        b.UnitUSD = price * seconds

    case "rerank":
        // Cohere-style: billed per search unit (1 per query)
        price := lookupPrice(ch.ID, endpoint, req.Model, "per_search_unit", 1)
        b.UnitUSD = price

    case "embedding":
        // token-based, like chat but cheaper
        b.InputUSD = float64(resp.Usage.PromptTokens) * ch.InputPriceUSD / 1_000_000
    }
    b.TotalUSD = b.InputUSD + b.OutputUSD + b.UnitUSD
    return b
}
```

### 6.5 Hidden params headers (LiteLLM pattern)

```go
// internal/multimodal/response.go
func WriteHiddenHeaders(w http.ResponseWriter, ch *model.Channel, resp *Response, err error) {
    h := w.Header()
    if resp != nil {
        if resp.ModelID != ""    { h.Set("X-Model-Id", resp.ModelID) }
        if resp.CacheKey != ""   { h.Set("X-Cache-Key", resp.CacheKey) }
        if resp.APIBase != ""    { h.Set("X-Upstream-Api-Base", resp.APIBase) }
        h.Set("X-Cost-USD", strconv.FormatFloat(resp.Cost.TotalUSD, 'f', 6, 64))
        h.Set("X-Request-Id", resp.RequestID)
    }
}
```

## 7. Wire format (per endpoint)

### 7.1 `/v1/embeddings`

```jsonc
// Request
{ "model": "text-embedding-3-small", "input": ["hello", "world"],
  "encoding_format": "float", "dimensions": 1536, "user": "u-123" }

// Response
{ "object": "list",
  "data": [{"object":"embedding","index":0,"embedding":[0.01,...]}],
  "model": "text-embedding-3-small-v2",
  "usage": {"prompt_tokens": 2, "total_tokens": 2} }
```

### 7.2 `/v1/images/generations`

```jsonc
// Request
{ "model": "gpt-image-1", "prompt": "a cat", "n": 1,
  "size": "1024x1024", "response_format": "url", "user": "u-123" }

// Response (url)
{ "created": 1700000000, "data": [{"url":"https://...","revised_prompt":"A cute cat"}] }

// Response (b64_json)
{ "created": 1700000000, "data": [{"b64_json":"iVBORw0K...","revised_prompt":"..."}] }
```

### 7.3 `/v1/images/edits` (multipart)

```
POST /v1/images/edits
Content-Type: multipart/form-data; boundary=----xxx
Authorization: Bearer sk-xxx

------xxx
Content-Disposition: form-data; name="image"; filename="cat.png"
Content-Type: image/png

<binary>
------xxx
Content-Disposition: form-data; name="mask"; filename="mask.png"
Content-Type: image/png

<binary>
------xxx
Content-Disposition: form-data; name="prompt"

a cat with a hat
------xxx
Content-Disposition: form-data; name="model"

gpt-image-1
------xxx
Content-Disposition: form-data; name="n"

1
------xxx
Content-Disposition: form-data; name="size"

1024x1024
------xxx--
```

Response: same shape as generations.

### 7.4 `/v1/audio/transcriptions` (multipart)

```
POST /v1/audio/transcriptions
Content-Type: multipart/form-data

file=@recording.mp3
model=whisper-1
language=en
response_format=json
```

Response:
```jsonc
{ "text": "Hello, world!" }
```

Optional formats: `text`, `srt`, `verbose_json`.

### 7.5 `/v1/audio/speech` (JSON → binary stream)

```jsonc
// Request
{ "model": "tts-1", "input": "Hello, world!", "voice": "alloy",
  "response_format": "mp3", "speed": 1.0 }

// Response
HTTP/1.1 200 OK
Content-Type: audio/mpeg
Content-Length: 12345
<binary mp3 bytes>
```

### 7.6 `/v1/rerank` (Cohere v2 schema)

```jsonc
// Request
{ "model": "rerank-english-v3.0",
  "query": "What is the capital of the US?",
  "documents": ["Carson City is the capital of Nevada.",
                "Washington DC is the capital of the US.",
                "Paris is the capital of France."],
  "top_n": 3 }

// Response
{ "id": "rerank-abc",
  "results": [
    {"index": 1, "relevance_score": 0.999},
    {"index": 0, "relevance_score": 0.327}
  ],
  "meta": { "billed_units": { "search_units": 1 } } }
```

### 7.7 `/v1/images/variations` (multipart)

```
POST /v1/images/variations
file=@image.png
model=dall-e-2
n=2
size=512x512
response_format=url
```

Response: same shape as generations.

## 8. Routing

```go
// internal/api/multimodal.go (extends existing router)
func (h *Handler) routeByEndpoint(endpoint string) (*model.Channel, error) {
    candidates := h.channels.Filter(func(c *model.Channel) bool {
        return c.Endpoint == endpoint && c.Enabled
    })
    if len(candidates) == 0 {
        return nil, fmt.Errorf("no enabled channel for endpoint %s", endpoint)
    }
    return h.thompson.Pick(candidates), nil  // reuse existing Thompson router
}
```

**Thompson sampling** stays per-endpoint. Each channel has independent
sampling state per endpoint (so image_gen traffic doesn't affect chat
quality sampling).

## 9. Rate limiting

Extend `internal/ratelimit` to support custom units:

```go
type AllowOpts struct {
    TokenID int64
    RPM     int               // per-minute
    TPM     int               // tokens per minute
    EstimatedTokens int       // for chat
    Units   int               // for image/audio/rerank (1 per call usually)
    UnitType string           // 'image' | 'audio_minute' | 'search_unit'
}

func (l *Limiter) Allow(opts AllowOpts) bool {
    // 1. RPM check (per-call)
    // 2. TPM check (token-based, only for chat/embed)
    // 3. Unit-type-specific check
    //    - 'image': per_image RPH
    //    - 'audio_minute': per-minute-of-audio RPH
    //    - 'search_unit': per-search RPH
}
```

For P9 we keep it simple: only RPM + estimated-cost-based TPM.
Image/audio/rerank each count as 1 RPM request. Cost is tracked
separately via `spend_logs`.

## 10. Tests

### 10.1 Unit tests (per package)

| Test | What |
|---|---|
| `multipart_test.go` | Parse JSON / Parse multipart / MIME inference from filename |
| `capabilities_test.go` | `supports()` matrix / graceful degradation / opencode parity |
| `cost_test.go` | per-token / per-image / per-char / per-second / per-search_unit |
| `response_test.go` | Hidden params headers / error mapping |
| `embeddings_test.go` | Mock OpenAI embeddings endpoint / multi-input / dimensions |
| `images_test.go` | generations / edits / variations / b64 vs url |
| `audio_test.go` | transcription (json/text/srt) / speech (binary stream) |
| `rerank_test.go` | Cohere schema parity / top_n / relevance_score range |
| `provider/openai_test.go` | All 6 new methods against `httptest` mock |
| `provider/anthropic_test.go` | Embed (not supported → ErrUnsupported) |
| `provider/gemini_test.go` | Embed + image_gen |

### 10.2 E2E tests (httptest)

```go
// internal/api/multimodal_e2e_test.go
func TestImagesGenerations_E2E(t *testing.T) {
    // 1. Set up mock upstream returning b64_json
    mock := httptest.NewServer(handler returning canned OpenAI image response)
    // 2. Register channel pointing at mock
    // 3. POST /v1/images/generations via testhelper
    // 4. Assert 200 + body shape + hidden params headers
    // 5. Assert logs row with cost > 0
    // 6. Assert endpoint_prices lookup used (cost = price * n)
}
```

Same pattern for all 7 new endpoints. ~7 e2e tests + ~40 unit tests.

## 11. Acceptance criteria

| Metric | Target |
|---|---|
| All 8 new endpoints respond (200 OK in e2e) | ✅ |
| Multipart parsing handles `image[]` (multiple) | ✅ |
| Multipart parsing handles `mask` (optional) | ✅ |
| Per-unit cost calculated for all 8 endpoints | ✅ |
| Per-token cost for chat/embedding (unchanged) | ✅ |
| Hidden params headers present on all responses | ✅ |
| Capability graceful degradation: unsupported modality → error text | ✅ |
| Channel.endpoint selector routes correctly | ✅ |
| Rate limit (RPM) per channel / per token | ✅ |
| `provider.Capabilities()` returns supported endpoints | ✅ |
| Coverage ≥ 70 % for `internal/multimodal`, `internal/api/{embeddings,images,audio,rerank}.go` | ✅ |
| Coverage overall ≥ 65 % | ✅ |
| Race-clean | ✅ |

## 12. Files to add / touch

```
NEW
internal/multimodal/multipart.go        ~150 LoC
internal/multimodal/multipart_test.go   ~80 LoC
internal/multimodal/capabilities.go     ~100 LoC
internal/multimodal/capabilities_test.go~80 LoC
internal/multimodal/cost.go             ~150 LoC
internal/multimodal/cost_test.go        ~100 LoC
internal/multimodal/response.go         ~80 LoC
internal/multimodal/response_test.go    ~50 LoC
internal/api/multimodal.go              ~200 LoC
internal/api/embeddings.go              ~250 LoC
internal/api/embeddings_test.go         ~100 LoC
internal/api/images.go                  ~350 LoC
internal/api/images_test.go             ~150 LoC
internal/api/audio.go                   ~400 LoC
internal/api/audio_test.go              ~150 LoC
internal/api/rerank.go                  ~250 LoC
internal/api/rerank_test.go             ~100 LoC
internal/api/multimodal_e2e_test.go     ~300 LoC
internal/provider/openai/multimodal.go  ~500 LoC
internal/provider/anthropic/multimodal.go ~100 LoC
internal/provider/gemini/multimodal.go  ~200 LoC
internal/model/types.go                 +Capabilities struct
internal/store/sqlite.go                +endpoint_prices table + CRUD
internal/store/store.go                 +CRUD signatures
internal/admin/handler.go               +CRUD endpoints
internal/ratelimit/ratelimit.go         +UnitType support
web/src/pages/Channels.tsx              +Endpoint / capabilities / endpoint_prices UI
web/src/pages/Settings.tsx              +Capabilities matrix preview
docs/P9-MULTIMODAL.md                   this file

MODIFY
internal/api/router.go                  +register 7 new routes
internal/api/router.go                  +routeByEndpoint()
internal/provider/provider.go           +Embed/Image/Audio/Rerank interfaces
internal/model/types.go                 +Channel.Endpoint / Channel.Capabilities
internal/store/sqlite.go                +channels.endpoint, channels.capabilities migrations
internal/ratelimit/ratelimit_test.go    +UnitType tests
```

Total new LoC: **~4150**.

## 13. New dependencies

**None.** All needed functionality is in stdlib
(`net/http`, `mime/multipart`, `encoding/json`, `io`, `unicode/utf8`)
or already vendored.

## 14. Rollout

1. Land schema migration (channels.endpoint, channels.capabilities,
   endpoint_prices table) + small CRUD in admin.
2. Land `internal/multimodal/*` (multipart + capabilities + cost +
   response) with full unit tests.
3. Land provider interface extension + OpenAI implementation.
4. Land `/v1/embeddings` (simplest) — verify end-to-end.
5. Land `/v1/images/generations` + `/v1/images/edits` +
   `/v1/images/variations`.
6. Land `/v1/audio/transcriptions` + `/v1/audio/speech` +
   `/v1/audio/translations`.
7. Land `/v1/rerank`.
8. Add Anthropic + Gemini partial support (embed + image_edit for
   Anthropic, embed + image_gen for Gemini).
9. Web UI: Channel form adds endpoint selector + capabilities matrix
   auto-derived from endpoint_prices catalog.
10. README + CHANGELOG.

## 15. Risks

| Risk | Mitigation |
|---|---|
| Multipart parsing edge cases (boundary in body) | Use `mime.ParseMediaType` + `multipart.NewReader`; test with adversarial inputs |
| File size DoS (10 MB image × 100 concurrent) | Cap `r.Body` via `http.MaxBytesReader` per-endpoint |
| Binary streaming backpressure (TTS) | Stream directly via `io.Copy` with `Flush()` per chunk |
| Provider diverges from OpenAI schema (Anthropic image_edit) | Capability check + graceful degradation |
| Upstream billing mismatch | Compare `cost_usd` from response vs our calc; alert on >5% delta |
| Modalities.json drift from models.dev catalog | Ship with v1 catalog; admins can override per channel |

## 16. Future (P9.5+)

- **Video endpoints** (`/v1/videos`) — wait for Sora 2 API to stabilize.
- **Image variations** — could be deprecated (DALL-E 2 only).
- **Audio translations** — small win, ship if cheap.
- **Embeddings images** — Gemini 2 multimodal embeddings.
- **Auto-cache mode** — opencode-style automatic cache_control injection.
- **Responses API** — GPT-5 native format (large surface).
- **MCP stdio transport** — see `docs/P11-MCP.md`.