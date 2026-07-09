// Package logstore provides the storage backend for request logs.
//
// Logs are written into per-date SQLite files (data/logs/YYYY-MM-DD.db)
// so retention is a `rm` instead of a slow DELETE, and so per-day files
// stay small enough for fast admin queries. Cross-day queries use
// SQLite ATTACH to union across all retained files transparently.
//
// Only the Driver interface is exported; the SQLiteDriver is the
// sole implementation today. The interface exists so future
// backends (DuckDB, columnar, etc.) can be added without touching
// the call sites in api or admin handlers.
package logstore

import (
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// Driver is the storage backend for log entries. Implementations
// must be safe for concurrent use; the Manager assumes a single
// Driver instance shared across goroutines.
type Driver interface {
	// Open initializes the backend rooted at dir. dir is created
	// if missing. Calling Open twice on the same Driver is a
	// programming error.
	Open(dir string) error

	// Insert appends one log entry. The driver MUST route to the
	// physical file corresponding to entry.CreatedAt's date, not
	// wall-clock time.
	Insert(entry *model.Log) error

	// QueryAcross returns rows matching filter across the given
	// days. If days is empty the driver MUST scan every file it
	// currently knows about (i.e. every undeleted day file).
	QueryAcross(filter QueryFilter, days []string) ([]model.Log, int64, error)

	// LogStats aggregates token/cost/error totals across the given
	// days. days=nil means "all files".
	LogStats(days []string) (LogStatsResult, error)

	// TimeSeries groups matching rows into time buckets of
	// bucketSec seconds, returning counts and token/cost totals per
	// bucket across the given days.
	TimeSeries(filter QueryFilter, bucketSec int64, days []string) ([]SeriesBucket, error)

	// TopByField groups matching rows by the given column name
	// (currently "model", "channel_id", "token_id") and returns the
	// top `limit` groups by row count.
	TopByField(filter QueryFilter, field string, limit int, days []string) ([]NamedMetric, error)

	// ListFiles returns all current log file identifiers (the
	// "YYYY-MM-DD" or "YYYY-MM-DD-N" basename without the .db
	// extension), sorted ascending.
	ListFiles() ([]string, error)

	// DeleteFiles removes the named files (closing any open
	// connections first). Idempotent: missing files are not an
	// error.
	DeleteFiles(days []string) error

	// Close releases all open connections. Subsequent Insert or
	// Query calls return an error.
	Close() error
}

// QueryFilter mirrors store.LogFilter. Drivers translate as needed;
// the Manager fills this from a store.LogFilter at the boundary.
type QueryFilter struct {
	TokenID     int64
	ChannelID   int64
	KeyID       int64
	Model       string
	StatusCode  int   // 0 = no filter
	CreatedFrom int64 // unix seconds, 0 = no lower bound
	CreatedTo   int64 // unix seconds, 0 = no upper bound
	Limit       int   // 0 = driver default (50)
	Offset      int   // 0 = first page
}

// LogStatsResult mirrors store.LogStats. Defined here to avoid a
// circular import (store imports logstore).
type LogStatsResult struct {
	PromptTokens     int64
	CompletionTokens int64
	RealCostUSD      float64
	BilledCostUSD    float64
	Total            int64
	Errors           int64
}

// SeriesBucket mirrors store.SeriesPoint: one aggregated bucket
// of a time-series query.
type SeriesBucket struct {
	Bucket           int64   `json:"bucket"` // unix seconds at bucket start
	Requests         int64   `json:"requests"`
	Errors           int64   `json:"errors"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	RealCostUSD      float64 `json:"real_cost_usd"`
	BilledCostUSD    float64 `json:"billed_cost_usd"`
}

// NamedMetric is a (label, value) pair used by TopByField.
type NamedMetric struct {
	Label  string  `json:"label"`
	Count  int64   `json:"count"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// MaxAttachFiles caps how many files a single QueryAcross may
// ATTACH at once. SQLite has a default limit of 10 attached
// databases; raising it requires a compile-time flag, so we cap
// here instead.
const MaxAttachFiles = 8

// Now is overridable in tests for deterministic day routing.
var Now = func() time.Time { return time.Now().UTC() }
