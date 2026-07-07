# P10 — Observability (Prometheus + OpenTelemetry)

> Date: 2026-07 · Owner: llmRx maintainers · Status: design.
> Target: after P8 + P9 land.

## 1. Why now

Today llmRx has **no way to be monitored from outside**:

| Need | Today | After P10 |
|---|---|---|
| Scrape metrics into Prometheus | ❌ no `/metrics` endpoint | ✅ standard Prometheus exposition |
| Alert on P95 latency > 1 s | ❌ can't; need to read dashboard | ✅ Prometheus rule on `http_request_duration_seconds{quantile="0.95"}` |
| Distributed tracing | ❌ no spans | ✅ OTel spans emitted to OTLP collector |
| Cross-instance aggregation | ❌ each instance is a black box | ✅ all instances push to the same backend |
| Audit / compliance trail | ⚠️ logs only, no export | ✅ OTel log exporter to Loki / Datadog |
| Vendor integration | ❌ Datadog / Honeycomb / SigNoz / Grafana Cloud — none | ✅ OTLP works with all of them |

Without this, llmRx is excluded from any commercial deployment that
runs Prometheus. That's the entire enterprise market.

## 2. Scope

| Feature | In scope | Out of scope |
|---|:---:|---|
| `GET /metrics` (Prometheus exposition) | ✅ | — |
| Default metric set (RED + USE) | ✅ | — |
| OpenTelemetry SDK init | ✅ | — |
| OTLP/gRPC + OTLP/HTTP exporters | ✅ | — |
| Trace context propagation (W3C `traceparent`) | ✅ | — |
| Per-request span (`route → upstream`) | ✅ | — |
| Configurable sampling rate | ✅ | — |
| Span attributes (channel / token / model / cost) | ✅ | — |
| Cardinality guards (drop high-cardinality labels) | ✅ | — |
| Datadog / Jaeger exporters | parked | ✅ (P10.5) |
| Continuous profiling (pprof endpoint) | parked | ✅ (P10.5) |

## 3. Prometheus metrics

Default RED+USE set, exported via `prometheus/client_golang`. Each
metric has HELP + TYPE annotations.

```
# RED — Rate / Errors / Duration
http_requests_total{method, route, status_class}        counter
http_request_duration_seconds{method, route, status_class}  histogram (5..30000ms)

# Capacity — Channels / Tokens / Provider health
channel_requests_total{channel_id, channel_name, model, status_class}  counter
channel_active_channels{status="enabled|disabled"}                       gauge
channel_circuit_open{channel_id, channel_name}                          gauge

# Routing layers — L1..L5
router_decisions_total{layer="L1|L2|L3|L4|L5", outcome}  counter

# Spend
tokens_billed_usd_total{token_id, plan_id, channel_id}      counter
tokens_prompt_total{token_id, channel_id, model}             counter
tokens_completion_total{token_id, channel_id, model}         counter
tokens_cached_total{token_id, channel_id, model}             counter

# Cache (P8)
cache_size{backend="memory|sqlite"}   gauge
cache_hits_total{backend}             counter
cache_misses_total{backend}           counter
cache_evictions_total{backend}       counter

# Alerts
alert_rules_loaded                gauge
alert_events_total{type, channel_id} counter
alert_webhooks_total{outcome="ok|fail"} counter

# SSE / broker
broker_subscribers                gauge
broker_drops_total{reason}         counter
broker_publishes_total             counter

# Streaming
streaming_active_gauge             gauge
streaming_timeout_total            counter
streaming_max_body_hit_total       counter

# Ratelimit
ratelimit_blocked_total{reason="rpm|tpm"}  counter

# Process
go_goroutines     gauge   (from promhttp)
go_memstats_*      gauge   (from promhttp)
```

All counters are **monotonic**, all histograms have bounded bucket
bounds (5 ms..30 s) to control cardinality.

### 3.1 Cardinality guards

`channel_id` and `token_id` are bounded (≤ 1000 channels, ≤ 10000
tokens), so they're safe to expose as labels. We drop high-cardinality
fields like `request_id` or `client_ip` — those go in spans, not
metrics.

### 3.2 Auth on `/metrics`

Two options:
1. Unauthenticated, bind to internal-only interface (`-metrics-addr
   127.0.0.1:9090`).
2. Authenticated via Bearer admin token.

**Decision**: default is option 1. Most production deployments have
a sidecar pattern (Prometheus scrapes a localhost port). Option 2 is
configurable via `metrics_auth_token` env var.

## 4. OpenTelemetry

### 4.1 SDK setup

```go
// internal/observability/tracer.go
package observability

func Init(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) {
    res, _ := resource.New(ctx,
        resource.WithAttributes(
            semconv.ServiceName("llmrx"),
            semconv.ServiceVersion(version.Version),
            attribute.String("deployment.environment", cfg.Environment),
        ),
        resource.WithProcess(),
        resource.WithHost(),
    )

    var exp trace.SpanExporter
    switch cfg.Exporter {
    case "otlp-grpc":
        exp, _ = otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint))
    case "otlp-http":
        exp, _ = otlptracehttp.New(ctx, otlptracehttp.WithEndpoint(cfg.OTLPEndpoint))
    case "stdout":
        exp = stdouttrace.New(stdouttrace.WithPrettyPrint())
    case "noop", "":
        return func(context.Context) error { return nil }, nil
    }

    tp := sdktrace.NewTracerProvider(
        sdktrace.WithBatcher(exp),
        sdktrace.WithResource(res),
        sdktrace.WithSampler(sdktrace.TraceIDRatioBased(cfg.SampleRate)), // default 0.01 = 1 %
    )
    otel.SetTracerProvider(tp)
    return tp.Shutdown, nil
}
```

### 4.2 Server config

```go
type ServerConfig struct {
    // ...
    MetricsAddr        string  `yaml:"metrics_addr"`         // "" = disabled; "127.0.0.1:9090" = sidecar pattern
    MetricsAuthToken   string  `yaml:"metrics_auth_token"`   // "" = unauthenticated
    
    OTelExporter       string  `yaml:"otel_exporter"`        // "" | "otlp-grpc" | "otlp-http" | "stdout"
    OTelEndpoint       string  `yaml:"otel_endpoint"`        // "otel-collector:4317" or "...:4318"
    OTelSampleRate     float64 `yaml:"otel_sample_rate"`      // 0..1, default 0.01
    OTelEnvironment    string  `yaml:"otel_environment"`     // "prod" | "staging" | etc.
}
```

### 4.3 Span shape

One span per request, attributes:

```
span: "POST /v1/chat/completions"
  attributes:
    http.method = "POST"
    http.route = "/v1/chat/completions"
    http.status_code = 200
    llmrx.token_id = 7
    llmrx.token_name = "prod"
    llmrx.channel_id = 2
    llmrx.channel_name = "openai-prod"
    llmrx.model = "gpt-4"
    llmrx.route_layer = "L1(static) → L2(breaker) → L3(cost) → L5(thompson) → select=c"
    llmrx.cached = false
    llmrx.prompt_tokens = 100
    llmrx.completion_tokens = 50
    llmrx.cached_tokens = 0
    llmrx.real_cost_usd = 0.0015
    llmrx.billed_cost_usd = 0.0018
  events:
    "ratelimit.allow"     { rpm=120, tpm=100000, decision="allow" }
    "router.layer"       { layer="L2", decision="open" }
    "upstream.request"    { base_url="...", provider="openai" }
```

### 4.4 Context propagation

The client can send `traceparent: 00-<traceid>-<spanid>-01` (W3C
Trace Context). The middleware extracts it and starts a child span
rather than a root span. Same for `tracestate` (W3C) and
`X-Request-ID` (custom).

## 5. Wire-up

### 5.1 Prometheus

```go
// internal/server/server.go (after Prometheus import)
import "github.com/prometheus/client_golang/prometheus/promhttp"

func (s *Server) registerMetricsServer() *http.Server {
    if s.cfg.Server.MetricsAddr == "" {
        return nil
    }
    mux := http.NewServeMux()
    mux.Handle("/metrics", s.metricsAuth(s.cfg.Server.MetricsAuthToken)(promhttp.Handler()))
    return &http.Server{
        Addr:    s.cfg.Server.MetricsAddr,
        Handler: mux,
        ReadHeaderTimeout: 5 * time.Second,
    }
}

// cmd/gateway/main.go
go func() {
    if srv := gateway.registerMetricsServer(); srv != nil {
        if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            log.Printf("metrics: %v", err)
        }
    }
}()
```

### 5.2 OpenTelemetry

Three injection points:

```go
// 1. ChatCompletions / streaming
ctx, span := otel.Tracer("llmrx.api").Start(r.Context(), "chat.completions")
defer span.End()

// 2. Provider HTTP call
ctx, span := otel.Tracer("llmrx.provider").Start(ctx, "openai.chat")
defer span.End()

// 3. Cache lookup (P8)
ctx, span := otel.Tracer("llmrx.cache").Start(ctx, "cache.get")
defer span.End()
```

Each span records attributes from §4.3 and ends with `span.SetStatus(codes.Ok)` or `codes.Error`.

### 5.3 Cardinality guards

```go
// Example: chat_completions span
span.SetAttributes(
    attribute.Int64("llmrx.token_id", tokenID),
    attribute.Int("llmrx.prompt_tokens", usage.PromptTokens),
    attribute.String("llmrx.channel_name", channel.Name),  // bounded; OK
)
// Do NOT add:
//   - client_ip    (unbounded; high cardinality)
//   - request_id   (unique per request; useless in aggregate)
//   - user_prompt  (PII + unbounded size)
```

## 6. Files to add / touch

```
internal/observability/tracer.go        # OTel SDK init + shutdown
internal/observability/metrics.go       # Prometheus metric registry
internal/observability/middleware.go    # Gin / chi middleware that records HTTP metrics
internal/api/router.go                  # start span on ChatCompletions
internal/provider/adapter.go            # start span on provider HTTP call
internal/cache/cache.go                 # start span on cache Get / Set (P8)
internal/server/server.go               # metrics + tracer wiring
internal/config/config.go               # 7 new yaml keys
cmd/gateway/main.go                     # init tracer + metrics server
go.mod                                  # +go.opentelemetry.io/otel, +prometheus/client_golang
internal/observability/tracer_test.go
internal/observability/metrics_test.go
```

## 7. Test plan

* **Metrics registry**: every metric is registered; sample values
  exposed via `prometheus/testutil.ToFloat64`.
* **Middleware**: each handler increments the right counter; the
  right histogram bucket is selected.
* **Tracer init**: exporter selection (`otlp-grpc | otlp-http | stdout
  | noop`) all work without panicking; sampler respects
  `SampleRate`.
* **W3C propagation**: incoming `traceparent` produces a child span.
* **Cardinality guards**: assert that no metric label can blow up
  by feeding synthetic channels/tokens.

## 8. Acceptance criteria

| Metric | Target |
|---|---|
| `GET /metrics` returns valid Prometheus text exposition | ✅ |
| All counters / histograms registered | ✅ |
| One span per `/v1/chat/completions` request | ✅ |
| Span attributes populated per §4.3 | ✅ |
| OTel exporter selectable via yaml | ✅ |
| W3C trace context propagation works | ✅ |
| Coverage ≥ 70 % for `internal/observability` | ✅ |
| Coverage overall ≥ 65 % | ✅ |
| Documentation includes Grafana dashboard JSON | ✅ |

## 9. Rollout

1. Land Prometheus metrics + `/metrics` endpoint.
2. Land OTel SDK init + span on `ChatCompletions` + `Provider.Chat`.
3. Land OTel provider-HTTP-call child span.
4. Land OTel cache span (P8).
5. Land span propagation (W3C).
6. Grafana dashboard template.
7. README + CHANGELOG.
8. Optional: Datadog exporter, pprof endpoint (P10.5).

## 10. Risks

| Risk | Mitigation |
|---|---|
| Cardinality explosion (unbounded labels) | Static label allow-list in `metrics.go`; tests assert. |
| OTel SDK adds 10-15 MB to binary | Use the otel SDK's "lite" mode (`WithResource` + minimal API surface) |
| Sampling drops the data you need | Default 1 %; configurable; `AlwaysSample` for debugging |
| Two metrics servers competing | Single server bound to `metrics_addr`; default `""` = disabled |
| Span volume overwhelms backend | Sampler default 1 %; configurable; OTLP exporter has built-in backpressure |

## 11. New dependencies

* `github.com/prometheus/client_golang` (stable, ~10k LOC)
* `go.opentelemetry.io/otel` + `sdk` + `exporters/otlp/otlptrace/otlptracegrpc`
  + `exporters/otlp/otlptrace/otlptracehttp` + `exporters/stdout/stdouttrace`

Pin to versions compatible with Go 1.18:
- otel v1.21.0+
- prometheus/client_golang v1.17.0+

Verify in CI: `go mod tidy && go test ./...`.