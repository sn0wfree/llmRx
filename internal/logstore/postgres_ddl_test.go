package logstore

import (
	"strings"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/dialect"
	"github.com/sn0wfree/llmRx/internal/model"
)

// TestPostgresDDL_TranslatedSchemaPinned pins the exact Postgres DDL
// produced by translating the canonical logsTableDDL. The Postgres
// driver and the SQLite driver now share one schema source; this test
// guards the translation so a dialect change that would silently
// diverge the two schemas fails here instead of in production.
//
// Known intentional delta vs the former hand-written Postgres DDL:
// token_id/channel_id/key_id/duration_ms are INTEGER (4-byte) instead
// of BIGINT. Those values are row ids and per-request durations, far
// below the 2^31 bound; CREATE TABLE IF NOT EXISTS leaves existing
// production tables untouched.
func TestPostgresDDL_TranslatedSchemaPinned(t *testing.T) {
	want := `CREATE TABLE IF NOT EXISTS logs (
	id BIGSERIAL PRIMARY KEY,
	token_id INTEGER NOT NULL DEFAULT 0,
	channel_id INTEGER NOT NULL DEFAULT 0,
	key_id INTEGER NOT NULL DEFAULT 0,
	model TEXT NOT NULL DEFAULT '',
	prompt_tokens INTEGER NOT NULL DEFAULT 0,
	completion_tokens INTEGER NOT NULL DEFAULT 0,
	cached_tokens INTEGER NOT NULL DEFAULT 0,
	reasoning_tokens INTEGER NOT NULL DEFAULT 0,
	cache_hit INTEGER NOT NULL DEFAULT 0,
	real_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
	billed_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
	duration_ms INTEGER NOT NULL DEFAULT 0,
	status_code INTEGER NOT NULL DEFAULT 0,
	router_path TEXT NOT NULL DEFAULT '',
	request_ip TEXT NOT NULL DEFAULT '',
	endpoint TEXT NOT NULL DEFAULT '',
	units INTEGER NOT NULL DEFAULT 0,
	created_at BIGINT NOT NULL
)`
	got := dialect.Postgres{}.RewriteDDL(logsTableDDL)
	if got != want {
		t.Fatalf("translated DDL drifted:\n got: %s\nwant: %s", got, want)
	}
}

// TestLogsInsertSQL_BindsMatchArgs guards the shared INSERT: the
// placeholder count must equal the number of bound args, and the
// bound unix timestamp must come from CreatedAt.
func TestLogsInsertSQL_BindsMatchArgs(t *testing.T) {
	ph := strings.Count(logsInsertSQL, "?")
	entry := &model.Log{CreatedAt: time.Unix(1700000000, 0)}
	args := logInsertArgs(entry)
	if ph != len(args) {
		t.Fatalf("logsInsertSQL has %d placeholders but logInsertArgs binds %d values", ph, len(args))
	}
	if last, ok := args[len(args)-1].(int64); !ok || last != 1700000000 {
		t.Fatalf("last arg must be the created_at unix seconds, got %v (%T)", args[len(args)-1], args[len(args)-1])
	}
}

// TestLogsSchema_ColumnsMatchSelectList pins the read path: the DDL's
// column set must be exactly what logRowCols selects (what
// streamLogRows scans). A new column must land in both or admin
// queries break at runtime.
func TestLogsSchema_ColumnsMatchSelectList(t *testing.T) {
	var ddlCols []string
	for _, line := range strings.Split(logsTableDDL, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "CREATE TABLE") || line == ")" {
			continue
		}
		ddlCols = append(ddlCols, strings.Fields(line)[0])
	}
	selectSet := map[string]bool{}
	for _, c := range strings.Split(logRowCols, ", ") {
		selectSet[strings.TrimSpace(c)] = true
	}
	if len(ddlCols) != len(selectSet) {
		t.Fatalf("ddl has %d columns, select list has %d", len(ddlCols), len(selectSet))
	}
	for _, c := range ddlCols {
		if !selectSet[c] {
			t.Fatalf("column %q is in the DDL but missing from logRowCols", c)
		}
	}
}
