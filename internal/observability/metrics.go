package observability

import "github.com/prometheus/client_golang/prometheus"

// Metrics holds all Prometheus metric collectors for llmRx.
// Initialized once via New() and used by the instrument package.
type Metrics struct {
	// Counters
	RequestsTotal         *prometheus.CounterVec
	UpstreamErrorsTotal   *prometheus.CounterVec
	BilledUSDTotal        *prometheus.CounterVec
	PromptTokensTotal     *prometheus.CounterVec
	CompletionTokensTotal *prometheus.CounterVec
	RateLimitBlocksTotal  *prometheus.CounterVec
	RetriesTotal          *prometheus.CounterVec

	// Histograms
	RequestDuration *prometheus.HistogramVec

	// Gauges
	ActiveStreams   prometheus.Gauge
	ChannelsEnabled prometheus.Gauge
	TokensActive    prometheus.Gauge
}

// New creates and registers all Prometheus metrics.
func New() *Metrics {
	m := &Metrics{
		RequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmrx_requests_total",
				Help: "Total number of chat completion requests.",
			},
			[]string{"model", "status", "stream"},
		),
		UpstreamErrorsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmrx_upstream_errors_total",
				Help: "Total number of upstream errors.",
			},
			[]string{"model", "code"},
		),
		BilledUSDTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmrx_billed_usd_total",
				Help: "Total billed cost in USD.",
			},
			[]string{"model"},
		),
		PromptTokensTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmrx_prompt_tokens_total",
				Help: "Total prompt (input) tokens.",
			},
			[]string{"model"},
		),
		CompletionTokensTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmrx_completion_tokens_total",
				Help: "Total completion (output) tokens.",
			},
			[]string{"model"},
		),
		RateLimitBlocksTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmrx_rate_limit_blocks_total",
				Help: "Total rate limit blocks.",
			},
			[]string{"reason"},
		),
		RetriesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmrx_retries_total",
				Help: "Total retry attempts.",
			},
			[]string{"model"},
		),
		RequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "llmrx_request_duration_seconds",
				Help:    "Request duration in seconds.",
				Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
			},
			[]string{"model", "stream"},
		),
		ActiveStreams: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "llmrx_active_streams",
				Help: "Number of currently active streaming connections.",
			},
		),
		ChannelsEnabled: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "llmrx_channels_enabled",
				Help: "Number of enabled channels in the pool.",
			},
		),
		TokensActive: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "llmrx_tokens_active",
				Help: "Number of active tokens in the cache.",
			},
		),
	}

	prometheus.MustRegister(
		m.RequestsTotal,
		m.UpstreamErrorsTotal,
		m.BilledUSDTotal,
		m.PromptTokensTotal,
		m.CompletionTokensTotal,
		m.RateLimitBlocksTotal,
		m.RetriesTotal,
		m.RequestDuration,
		m.ActiveStreams,
		m.ChannelsEnabled,
		m.TokensActive,
	)

	return m
}
