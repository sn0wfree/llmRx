package observability

import (
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Global metrics instance. Initialized by Init() at startup.
var global *Metrics

// Init creates the global metrics instance. Must be called once
// at startup before any requests are served.
func Init() {
	if global != nil {
		return
	}
	global = New()
	log.Printf("metrics: initialized (prometheus collectors registered)")
}

// Handler returns an http.Handler that serves /metrics.
func Handler() http.Handler {
	if global == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("metrics not initialized"))
		})
	}
	return promhttp.Handler()
}

// RecordRequest increments counters and observes duration for a
// completed chat request. Called from emitLog.
func RecordRequest(model string, durationMs int64, failed bool, billedUSD float64, promptTokens, completionTokens int, stream bool) {
	if global == nil {
		return
	}
	status := "ok"
	if failed {
		status = "fail"
	}
	streamStr := "false"
	if stream {
		streamStr = "true"
	}

	global.RequestsTotal.WithLabelValues(model, status, streamStr).Inc()
	global.BilledUSDTotal.WithLabelValues(model).Add(billedUSD)
	global.PromptTokensTotal.WithLabelValues(model).Add(float64(promptTokens))
	global.CompletionTokensTotal.WithLabelValues(model).Add(float64(completionTokens))
	global.RequestDuration.WithLabelValues(model, streamStr).Observe(float64(durationMs) / 1000.0)
}

// RecordUpstreamError increments the upstream error counter.
func RecordUpstreamError(model string, statusCode int) {
	if global == nil {
		return
	}
	global.UpstreamErrorsTotal.WithLabelValues(model, httpStatusClass(statusCode)).Inc()
}

// RecordRateLimitBlock increments the rate limit block counter.
func RecordRateLimitBlock(reason string) {
	if global == nil {
		return
	}
	global.RateLimitBlocksTotal.WithLabelValues(reason).Inc()
}

// RecordRetry increments the retry counter for a model.
func RecordRetry(model string) {
	if global == nil {
		return
	}
	global.RetriesTotal.WithLabelValues(model).Inc()
}

// StreamStart increments the active streams gauge.
func StreamStart() {
	if global != nil {
		global.ActiveStreams.Inc()
	}
}

// StreamEnd decrements the active streams gauge.
func StreamEnd() {
	if global != nil {
		global.ActiveStreams.Dec()
	}
}

// SetChannelsEnabled sets the channels enabled gauge.
func SetChannelsEnabled(n float64) {
	if global != nil {
		global.ChannelsEnabled.Set(n)
	}
}

// SetTokensActive sets the active tokens gauge.
func SetTokensActive(n float64) {
	if global != nil {
		global.TokensActive.Set(n)
	}
}

// httpStatusClass returns a coarse status class label like "2xx",
// "4xx", "5xx".
func httpStatusClass(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 300 && code < 400:
		return "3xx"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500:
		return "5xx"
	default:
		return "other"
	}
}

// Now is a package-level function for testing; returns current time.
// Production code uses time.Now() directly.
var Now = time.Now
