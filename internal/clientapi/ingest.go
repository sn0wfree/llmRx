package clientapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/model"
)

// EdgeClients is the configured list of edge clients allowed to upload
// usage. Sourced from config.yml `edge_clients`.
type EdgeClient struct {
	Name  string `yaml:"name" json:"name"`
	Token string `yaml:"token" json:"token"`
}

// Ingestor accepts usage batches from edge clients and folds them into
// the server's log store, so edge traffic is visible in the same admin
// pages as direct traffic. It also serves the control plane
// (heartbeat + signed config) that edge clients poll.
type Ingestor struct {
	logStore *logstore.Manager
	// clients is the static config.yml list plus any client enrolled at
	// runtime. Guarded by mu because enroll appends to it.
	clients []EdgeClient
	// control holds the pull-based control plane state. nil disables
	// /client/v1/heartbeat and /client/v1/config.
	control *controlState
	// enrollStore persists enrolled clients and one-time codes (P15
	// phase 3). nil disables /client/v1/enroll.
	enrollStore EnrollStore

	// dedup guards against retried uploads double-counting rows. An
	// in-memory LRU of recent (client, request_id) pairs; a restart
	// clears it, which is acceptable for telemetry (worst case is a
	// duplicated observability row after a server restart, never a
	// lost one).
	mu   sync.Mutex
	seen map[string]time.Time
	// evictAfter bounds memory; entries older than this are pruned on
	// each ingest.
	evictAfter time.Duration
}

// NewIngestor wires the ingest endpoint. clients may be empty, which
// disables the endpoint entirely (404) rather than leaving it open.
func NewIngestor(ls *logstore.Manager, clients []EdgeClient) *Ingestor {
	ing := &Ingestor{
		logStore:   ls,
		clients:    clients,
		seen:       make(map[string]time.Time),
		evictAfter: 24 * time.Hour,
	}
	if len(clients) > 0 {
		ing.control = newControlState(clients)
	}
	return ing
}

// edgeUsageRecord is one row of an upload batch. Field names match the
// client's UsageRecord JSON so the wire format is symmetric.
type edgeUsageRecord struct {
	RequestID        string `json:"request_id"`
	Timestamp        int64  `json:"ts"`
	Model            string `json:"model"`
	Status           int    `json:"status"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	// Source: proxy (server already billed it — observability only),
	// cache (served locally, no tokens), local (served by an edge-side
	// fallback upstream — the server never saw it, so this row IS the
	// billable record).
	Source    string `json:"source"`
	LatencyMS int64  `json:"latency_ms"`
}

type edgeUsageBatch struct {
	ClientName string            `json:"client_name"`
	Records    []edgeUsageRecord `json:"records"`
}

// Handler returns the /client/v1 routes. All endpoints authenticate
// with a bearer token from the edge_clients list; an empty list mounts
// nothing at all, so a deployment without edge clients exposes no
// surface.
func (ing *Ingestor) Handler() http.Handler {
	r := http.NewServeMux()
	r.HandleFunc("/client/v1/usage", ing.handleUsage)
	r.HandleFunc("/client/v1/healthz", ing.handleHealth)
	r.HandleFunc("/client/v1/heartbeat", ing.handleHeartbeat)
	r.HandleFunc("/client/v1/config", ing.handleConfig)
	// Enrollment is unauthenticated by design: the one-time code is the
	// credential. It answers 503 when no registry is configured.
	r.HandleFunc("/client/v1/enroll", ing.handleEnroll)
	return r
}

// handleHeartbeat records liveness and tells the client whether its
// config is stale.
func (ing *Ingestor) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	name, token, ok := ing.authenticateFull(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, `{"error":{"code":"invalid_client","message":"unknown edge client token"}}`)
		return
	}
	// Persist last_seen for enrolled clients (best effort: telemetry for
	// the admin page must never fail a heartbeat).
	if c, found := ing.clientBySecret(token); found {
		_ = ing.enrollStore.TouchEdgeClient(c.ClientID, time.Now().UTC())
	}
	if ing.control == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "control plane disabled")
		return
	}
	ing.control.handleHeartbeat(w, r, name)
}

// handleConfig returns the signed config bundle for this client.
func (ing *Ingestor) handleConfig(w http.ResponseWriter, r *http.Request) {
	name, token, ok := ing.authenticateFull(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, `{"error":{"code":"invalid_client","message":"unknown edge client token"}}`)
		return
	}
	if ing.control == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "control plane disabled")
		return
	}
	ing.control.handleConfig(w, r, name, token)
}

// authenticate checks the bearer token against the configured edge
// clients, returning the client's name.
func (ing *Ingestor) authenticate(r *http.Request) (string, bool) {
	name, _, ok := ing.authenticateFull(r)
	return name, ok
}

// authenticateFull also returns the matched token, which the control
// plane needs as the HMAC key for the client's config signature.
//
// Resolution order: static config list, then the enrolled-client
// registry. A secret enrolled at runtime signs its own config, so the
// control plane keeps working across restarts without the operator
// editing config.yml.
func (ing *Ingestor) authenticateFull(r *http.Request) (name, token string, ok bool) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return "", "", false
	}
	got := strings.TrimPrefix(auth, "Bearer ")
	if got == "" {
		return "", "", false
	}
	name, ok = ing.verifyClient(got)
	if !ok {
		return "", "", false
	}
	return name, got, true
}

func equalFoldConstantTime(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// maxBatchBytes bounds one upload; a client batching 200 rows of a few
// hundred bytes stays well under it.
const maxBatchBytes = 8 << 20

// maxBatchRows bounds the row count for the same reason.
const maxBatchRows = 2000

func (ing *Ingestor) handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "usage ingest requires POST")
		return
	}
	clientName, ok := ing.authenticate(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, `{"error":{"code":"invalid_client","message":"unknown edge client token"}}`)
		return
	}

	var batch edgeUsageBatch
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if len(batch.Records) == 0 {
		writeJSONError(w, http.StatusBadRequest, "empty records")
		return
	}
	if len(batch.Records) > maxBatchRows {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "too many records")
		return
	}

	// The authenticated client's configured name wins over the body's
	// self-reported one: records are attributed by credential, not by
	// whatever the sender claims.
	name := clientName

	accepted, duplicates := 0, 0
	for i := range batch.Records {
		rec := &batch.Records[i]
		if rec.RequestID == "" || !validSource(rec.Source) {
			continue
		}
		if ing.alreadySeen(name, rec.RequestID) {
			duplicates++
			continue
		}
		if ing.logStore != nil {
			_ = ing.logStore.Insert(toLog(name, rec))
		}
		accepted++
	}

	logging.Info("edge usage ingested",
		logging.F("client", name),
		logging.F("accepted", accepted),
		logging.F("duplicates", duplicates),
		logging.F("rejected", len(batch.Records)-accepted-duplicates),
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"accepted":    accepted,
		"duplicates":  duplicates,
		"client_name": name,
	})
}

// alreadySeen reports (and records) whether this client/request pair
// was ingested recently.
func (ing *Ingestor) alreadySeen(client, requestID string) bool {
	key := client + "/" + requestID
	now := time.Now()

	ing.mu.Lock()
	defer ing.mu.Unlock()
	if t, ok := ing.seen[key]; ok && now.Sub(t) < ing.evictAfter {
		ing.seen[key] = now // refresh so an active key is not evicted
		return true
	}
	ing.seen[key] = now
	// Amortised prune.
	if len(ing.seen) > 100000 {
		for k, t := range ing.seen {
			if now.Sub(t) > ing.evictAfter {
				delete(ing.seen, k)
			}
		}
	}
	return false
}

// toLog maps an edge record into the shared log model so it renders in
// the existing Logs page and Analytics. RouterPath carries the edge
// identity ("client:<name>") and Source distinguishes billable rows.
func toLog(clientName string, rec *edgeUsageRecord) *model.Log {
	ts := time.Now().UTC()
	if rec.Timestamp > 0 {
		ts = time.Unix(rec.Timestamp, 0).UTC()
	}
	return &model.Log{
		Model:            rec.Model,
		PromptTokens:     rec.PromptTokens,
		CompletionTokens: rec.CompletionTokens,
		RealCostUSD:      rec.CostUSD,
		BilledCostUSD:    rec.CostUSD,
		DurationMs:       rec.LatencyMS,
		StatusCode:       rec.Status,
		RouterPath:       "client:" + clientName,
		RequestIP:        "edge:" + clientName,
		CreatedAt:        ts,
	}
}

func validSource(s string) bool {
	switch s {
	case "proxy", "cache", "local":
		return true
	}
	return false
}

func (ing *Ingestor) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "clients": len(ing.clients)})
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	if !strings.HasPrefix(strings.TrimSpace(msg), "{") {
		msg = `{"error":{"message":"` + msg + `"}}`
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
