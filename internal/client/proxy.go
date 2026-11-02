package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sn0wfree/llmRx/internal/cache"
	"github.com/sn0wfree/llmRx/internal/provider"
)

// httpStatusRequestTimeout mirrors net/http's constant (avoiding the
// import alias noise for one value).
const httpStatusRequestTimeout = 408

// defaultChatPath is the OpenAI-compatible endpoint this client serves.
const chatCompletionsPath = "/v1/chat/completions"

// Proxy is the edge client's data plane: a local HTTP server that
// forwards OpenAI-compatible requests to the configured llmRx server,
// with a local response cache, an offline fallback chain and usage
// accounting.
type Proxy struct {
	cfg    *Config
	client *http.Client
	cache  *Cache
	brk    *breaker
	buffer *usageBuffer

	// serverIndex round-robins the configured servers on transport
	// failure so a dead primary fails over to the next entry.
	serverIndex atomic.Int64

	// requestSeq generates usage request ids.
	requestSeq atomic.Int64
	startedAt  time.Time
	// usageSink persists usage rows across restarts; wired by the
	// binary after construction. nil keeps everything in memory.
	usageSink UsagePersister
	// control is the optional config poller (nil when no client token
	// is configured). Only read by /healthz.
	control *Control
}

// NewProxy wires the data plane.
func NewProxy(cfg *Config) *Proxy {
	return &Proxy{
		cfg:       cfg,
		client:    &http.Client{},
		cache:     NewCache(cfg.CacheMaxItems),
		brk:       newBreaker(3, 30*time.Second),
		buffer:    newUsageBuffer(5000),
		startedAt: time.Now(),
	}
}

// Cache returns the response cache (status page, purge).
func (p *Proxy) Cache() *Cache { return p.cache }

// PendingUsage reports how many usage rows await upload.
func (p *Proxy) PendingUsage() int { return p.buffer.len() }

// ServerBreaker reports the reachability breaker state.
func (p *Proxy) ServerBreaker() (open bool, fails int) { return p.brk.state() }

// Uptime returns how long the proxy has been running.
func (p *Proxy) Uptime() time.Duration { return time.Since(p.startedAt) }

// Handler returns the local HTTP handler.
func (p *Proxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", p.handleChat)
	mux.HandleFunc("/healthz", p.handleLocalHealth)
	// Everything else forwards untouched (models, embeddings, ...).
	mux.HandleFunc("/", p.handlePassThrough)
	return mux
}

// SetControl wires the control-plane poller so /healthz can report its
// state.
func (p *Proxy) SetControl(c *Control) { p.control = c }

// Control returns the control-plane poller (nil when no client token is
// configured).
func (p *Proxy) Control() *Control { return p.control }

// handleLocalHealth is the client's own liveness endpoint, distinct
// from the server's /health which is proxied through "/".
func (p *Proxy) handleLocalHealth(w http.ResponseWriter, r *http.Request) {
	open, fails := p.brk.state()
	out := map[string]any{
		"status":        "ok",
		"server_open":   open,
		"server_fails":  fails,
		"pending_usage": p.buffer.len(),
		"cache":         p.cache.stats(r.Context()),
		"uptime_sec":    int(p.Uptime().Seconds()),
	}
	if p.control != nil {
		lastErr, lastOK := p.control.LastStatus()
		out["config_version"] = p.control.AppliedVersion()
		out["control_last_error"] = lastErr
		if !lastOK.IsZero() {
			out["control_last_ok"] = lastOK.UTC().Format(time.RFC3339)
		}
	}
	writeJSONObj(w, http.StatusOK, out)
}

// writeJSONObj serialises v as a JSON response.
func writeJSONObj(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// handlePassThrough forwards non-chat endpoints verbatim.
func (p *Proxy) handlePassThrough(w http.ResponseWriter, r *http.Request) {
	p.forwardRaw(w, r)
}

// handleChat serves /v1/chat/completions with cache + fallback +
// accounting. The order of operations matters:
//
//  1. parse the request (needed for the cache key and usage rows)
//  2. on cache hit → serve locally (source=cache)
//  3. forward to the server (source=proxy)
//  4. on transport failure / 5xx → fall back to the cache, then fail
func (p *Proxy) handleChat(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "chat/completions requires POST")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	var req provider.ChatRequest
	strictJSON := true
	if err := json.Unmarshal(body, &req); err != nil {
		// A body we cannot parse cannot be cache-keyed; forward as-is
		// and let the server produce the authoritative error.
		strictJSON = false
	}
	streaming := req.Stream
	cacheable := strictJSON && !streaming && (req.Temperature == nil || *req.Temperature == 0)

	var key string
	if cacheable {
		key, err = lookupKey(&req)
		if err != nil {
			// Non-deterministic request (temperature above zero) —
			// forward without caching.
			cacheable = false
		}
	}
	if cacheable {
		if e := p.cache.get(r.Context(), key); e != nil {
			// Cache hits are only served locally when they are fresh;
			// TTL is enforced by the cache itself, so an entry here is
			// within its window.
			writeCached(w, e)
			p.record(r, e, SourceCache, start, req.Model, 200)
			return
		}
	}

	// Forward. On a reachable server this is the whole story.
	respBody, status, transportErr, cachedEntry := p.forwardChat(r, body, cacheable)
	if errors.Is(transportErr, errUseRawPath) {
		// Streaming request: hand it to the unbuffered path so SSE
		// tokens flow through as they arrive.
		p.forwardRaw(w, r)
		return
	}
	if transportErr == nil && !fallible(status, nil) {
		if cacheable && cachedEntry != nil {
			p.cache.put(r.Context(), key, cachedEntry, time.Duration(p.cfg.CacheTTLSec)*time.Second)
		}
		copyResponse(w, respBody, status)
		p.record(r, nil, SourceProxy, start, req.Model, status)
		return
	}

	// Server unreachable or unhealthy: engage the fallback chain.
	p.brk.failure()
	if cacheable {
		if e := p.cache.get(r.Context(), key); e != nil {
			writeCached(w, e)
			p.record(r, e, SourceCache, start, req.Model, 200)
			return
		}
	}
	if transportErr != nil {
		writeJSONError(w, http.StatusServiceUnavailable,
			`{"error":{"code":"client_upstream_unreachable","message":"llmRx server unreachable and no cached response"}}`)
		p.record(r, nil, SourceProxy, start, req.Model, 0)
		return
	}
	// Server responded with 5xx: pass it through rather than inventing
	// a synthetic error.
	copyResponse(w, respBody, status)
	p.record(r, nil, SourceProxy, start, req.Model, status)
}

// forwardChat performs the outbound request. It returns the response
// body (fully read: chat responses are JSON, and reading lets us cache
// and account), the status, a transport error, and a cache entry when
// the response is cache-worthy.
//
// Streaming responses are handled by forwardRaw (the generic path);
// when stream=true this function is bypassed entirely so tokens flow
// through unbuffered.
func (p *Proxy) forwardChat(r *http.Request, body []byte, cacheable bool) ([]byte, int, error, *cache.Entry) {
	if streamingBody(body) {
		// Keep streaming on the raw path.
		return nil, 0, errUseRawPath, nil
	}
	resp, status, terr := p.attemptServers(r, body, false)
	if terr != nil {
		return nil, 0, terr, nil
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, 0, err, nil
	}
	// A response below 500 proves the server is alive (even a 4xx is
	// the server talking business); a 5xx counts toward the breaker.
	if status < 500 {
		p.brk.success()
	} else {
		p.brk.failure()
	}

	var entry *cache.Entry
	if cacheable && status == http.StatusOK {
		var pr provider.ChatResponse
		if jerr := json.Unmarshal(respBody, &pr); jerr == nil {
			entry = &cache.Entry{
				StatusCode: status,
				Body:       respBody,
				StoredAt:   time.Now().UTC(),
			}
			entry.Usage = &pr.Usage
		}
	}
	return respBody, status, nil, entry
}

// errUseRawPath is an internal signal, never returned to a caller.
var errUseRawPath = errors.New("client: use raw streaming path")

func streamingBody(body []byte) bool {
	var probe struct {
		Stream *bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return probe.Stream != nil && *probe.Stream
}

// attemptServers tries each configured server in order, rotating the
// start index after a full pass so a dead primary does not add latency
// to every request forever.
func (p *Proxy) attemptServers(r *http.Request, body []byte, raw bool) (*http.Response, int, error) {
	servers := p.cfg.ServersSnapshot()
	n := len(servers)
	if n == 0 {
		return nil, 0, errors.New("no servers configured")
	}
	start := int(p.serverIndex.Load()) % n
	var lastErr error
	for i := 0; i < n; i++ {
		idx := (start + i) % n
		base := strings.TrimRight(servers[idx], "/")
		target := base + r.URL.Path
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}

		req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
		if err != nil {
			return nil, 0, err
		}
		copyHeaders(req.Header, r.Header)
		// Identify the edge in server-side logs.
		if xff := r.Header.Get("X-Forwarded-For"); xff == "" {
			req.Header.Set("X-Forwarded-For", remoteIP(r))
		}
		req.Header.Set("X-Llmrx-Client", p.cfg.ClientName)

		// The auth header is injected only when the agent did not send
		// one: an explicit token always wins over the configured one.
		if req.Header.Get("Authorization") == "" && p.cfg.Token != "" {
			req.Header.Set("Authorization", "Bearer "+p.cfg.Token)
		}

		resp, err := p.client.Do(req)
		if err != nil {
			lastErr = err
			continue // next server
		}
		// A live server answered — remember it as the new primary.
		p.serverIndex.Store(int64(idx))
		return resp, resp.StatusCode, nil
	}
	if lastErr == nil {
		lastErr = errors.New("all servers failed")
	}
	return nil, 0, lastErr
}

// forwardRaw streams the exchange without buffering (SSE, audio).
func (p *Proxy) forwardRaw(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if !p.brk.allow(time.Now()) {
		writeJSONError(w, http.StatusServiceUnavailable,
			`{"error":{"code":"client_upstream_unreachable","message":"llmRx server unreachable"}}`)
		return
	}
	resp, status, terr := p.attemptServers(r, nil, true)
	if terr != nil {
		p.brk.failure()
		writeJSONError(w, http.StatusServiceUnavailable,
			`{"error":{"code":"client_upstream_unreachable","message":"llmRx server unreachable"}}`)
		return
	}
	defer resp.Body.Close()
	// Streaming from a <500 response proves liveness; a 5xx counts as
	// a failure just like the buffered path.
	if status < 500 {
		p.brk.success()
	} else {
		p.brk.failure()
	}

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(status)
	// Flush per chunk so SSE tokens stream through immediately.
	flusher, canFlush := w.(http.Flusher)
	_, _ = io.Copy(writerFunc(func(b []byte) (int, error) {
		n, err := w.Write(b)
		if canFlush {
			flusher.Flush()
		}
		return n, err
	}), resp.Body)

	if r.URL.Path == chatCompletionsPath {
		p.record(r, nil, SourceProxy, start, "", status)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

// record accumulates a usage row. best-effort telemetry: never fails
// the request.
func (p *Proxy) record(r *http.Request, e *cache.Entry, source string, start time.Time, model string, status int) {
	if !ValidSource(source) {
		source = SourceProxy
	}
	rec := UsageRecord{
		RequestID:        fmt.Sprintf("%s-%d", p.cfg.ClientName, p.requestSeq.Add(1)),
		Timestamp:        start.Unix(),
		Model:            model,
		Status:           status,
		Source:           source,
		LatencyMS:        time.Since(start).Milliseconds(),
	}
	if e != nil && e.Usage != nil {
		rec.PromptTokens = e.Usage.PromptTokens
		rec.CompletionTokens = e.Usage.CompletionTokens
	}
	p.buffer.add(rec)
}

// DrainUsage uploads accumulated rows to the server's ingest endpoint.
// Returns the number uploaded. Called by the periodic uploader and
// available for a manual flush.
func (p *Proxy) DrainUsage(ctx context.Context, client *http.Client) (int, error) {
	url := p.cfg.usageURL()
	if url == "" || p.cfg.ClientToken == "" {
		return 0, nil
	}
	batch := p.buffer.take(200)
	if len(batch) == 0 {
		return 0, nil
	}
	payload, err := json.Marshal(map[string]any{
		"client_name": p.cfg.ClientName,
		"records":     batch,
	})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.ClientToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		// Put the batch back for the next attempt.
		p.requeue(batch)
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		p.requeue(batch)
		return 0, fmt.Errorf("usage upload: status %d", resp.StatusCode)
	}
	return len(batch), nil
}

// requeue puts failed rows back at the front of the buffer, preserving
// order.
func (p *Proxy) requeue(batch []UsageRecord) {
	p.buffer.mu.Lock()
	defer p.buffer.mu.Unlock()
	p.buffer.pending = append(batch, p.buffer.pending...)
}

// StartUsageUploader runs the periodic upload loop until ctx ends.
func (p *Proxy) StartUsageUploader(ctx context.Context, client *http.Client) {
	interval := time.Duration(p.cfg.UploadBatchSec) * time.Second
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = p.DrainUsage(ctx, client)
		}
	}
}

// ---------- helpers ----------

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		switch k {
		case "Host", "Content-Length", "Connection":
			continue
		}
		for _, v := range vs {
			dst.Set(k, v)
		}
	}
}

func remoteIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		return host[:i]
	}
	return host
}

func writeCached(w http.ResponseWriter, e *cache.Entry) {
	h := w.Header()
	for k, v := range e.Headers {
		h.Set(k, v)
	}
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", "application/json")
	}
	h.Set("X-Llmrx-Cache", "hit")
	w.WriteHeader(e.StatusCode)
	_, _ = w.Write(e.Body)
}

func copyResponse(w http.ResponseWriter, body []byte, status int) {
	h := w.Header()
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", "application/json")
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	// msg is already a JSON object string in the unreachable paths; for
	// plain messages wrap it consistently.
	if !strings.HasPrefix(strings.TrimSpace(msg), "{") {
		msg = fmt.Sprintf(`{"error":{"message":%q}}`, msg)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg))
}
