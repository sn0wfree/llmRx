package logstore

import (
	"os"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// TestPostgresDriver exercises the full Driver contract against a
// real Postgres (shared cluster logs table). Skipped unless
// LLMRX_TEST_PG_DSN is set. Use a dedicated test database; the
// test drops the logs table at the start.
func TestPostgresDriver(t *testing.T) {
	dsn := os.Getenv("LLMRX_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set LLMRX_TEST_PG_DSN to run")
	}
	d, err := NewPostgresDriver(dsn)
	if err != nil {
		t.Fatalf("NewPostgresDriver: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// Fresh table.
	if _, err := d.db.Exec(`DROP TABLE IF EXISTS logs`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if err := d.Open("ignored"); err != nil {
		t.Fatalf("Open: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	base := now.Add(-2 * time.Hour)
	entries := []*model.Log{
		{TokenID: 1, ChannelID: 10, KeyID: 100, Model: "gpt-4o", PromptTokens: 100, CompletionTokens: 50, CachedTokens: 10, RealCostUSD: 0.01, BilledCostUSD: 0.012, DurationMs: 100, StatusCode: 200, RouterPath: "/v1/chat/completions", RequestIP: "1.1.1.1", Endpoint: "", Units: 1, CreatedAt: base},
		{TokenID: 1, ChannelID: 10, KeyID: 100, Model: "gpt-4o", PromptTokens: 200, CompletionTokens: 100, RealCostUSD: 0.02, BilledCostUSD: 0.024, DurationMs: 200, StatusCode: 500, RouterPath: "/v1/chat/completions", RequestIP: "1.1.1.1", Units: 1, CreatedAt: base.Add(30 * time.Second)},
		{TokenID: 2, ChannelID: 20, KeyID: 200, Model: "claude-3.5", PromptTokens: 50, CompletionTokens: 25, RealCostUSD: 0.005, BilledCostUSD: 0.006, DurationMs: 50, StatusCode: 200, RouterPath: "/v1/mcp", RequestIP: "2.2.2.2", Endpoint: "mcp", Units: 2, CreatedAt: base.Add(time.Hour)},
	}

	// Batch insert (transactional).
	n, err := d.BatchInsert(entries)
	if err != nil {
		t.Fatalf("BatchInsert: %v", err)
	}
	if n != 3 {
		t.Fatalf("BatchInsert n=%d, want 3", n)
	}

	// Single insert.
	extra := &model.Log{TokenID: 3, ChannelID: 30, KeyID: 300, Model: "gpt-4o-mini", PromptTokens: 10, StatusCode: 200, Units: 1, CreatedAt: now}
	if err := d.Insert(extra); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// QueryAcross: all.
	rows, total, err := d.QueryAcross(QueryFilter{Limit: 10}, nil)
	if err != nil {
		t.Fatalf("QueryAcross: %v", err)
	}
	if total != 4 || len(rows) != 4 {
		t.Fatalf("QueryAcross total=%d rows=%d, want 4/4", total, len(rows))
	}
	// Newest first.
	if rows[0].CreatedAt.Before(rows[1].CreatedAt) {
		t.Fatalf("QueryAcross not newest-first: %v then %v", rows[0].CreatedAt, rows[1].CreatedAt)
	}

	// QueryAcross: filter by token + model + status.
	rows, total, err = d.QueryAcross(QueryFilter{TokenID: 1, Model: "gpt-4o", StatusCode: 500, Limit: 10}, nil)
	if err != nil {
		t.Fatalf("QueryAcross filtered: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].StatusCode != 500 {
		t.Fatalf("filtered: total=%d rows=%d, want 1/1", total, len(rows))
	}

	// QueryAcross: pagination.
	rows, total, err = d.QueryAcross(QueryFilter{Limit: 2, Offset: 2}, nil)
	if err != nil || len(rows) != 2 || total != 4 {
		t.Fatalf("paginated: err=%v rows=%d total=%d", err, len(rows), total)
	}

	// QueryAcross: endpoint filter (MCP rows).
	rows, total, err = d.QueryAcross(QueryFilter{Endpoint: "mcp", Limit: 10}, nil)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].Units != 2 {
		t.Fatalf("endpoint filter: err=%v total=%d", err, total)
	}

	// LogStats.
	stats, err := d.LogStats(nil)
	if err != nil {
		t.Fatalf("LogStats: %v", err)
	}
	if stats.Total != 4 || stats.Errors != 1 {
		t.Fatalf("LogStats total=%d errors=%d, want 4/1", stats.Total, stats.Errors)
	}
	if stats.PromptTokens != 360 || stats.CompletionTokens != 175 {
		t.Fatalf("LogStats tokens: %d/%d, want 360/175", stats.PromptTokens, stats.CompletionTokens)
	}

	// TimeSeries: 1-hour buckets -> rows at base and base+1h.
	series, err := d.TimeSeries(QueryFilter{}, 3600, nil)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	// base (2 entries), base+1h (1), now=base+2h (1).
	if len(series) != 3 {
		t.Fatalf("TimeSeries buckets=%d, want 3", len(series))
	}
	if series[0].Requests != 2 || series[0].Errors != 1 {
		t.Fatalf("TimeSeries[0]: req=%d err=%d, want 2/1", series[0].Requests, series[0].Errors)
	}

	// TopByField: model.
	top, err := d.TopByField(QueryFilter{}, "model", 5, nil)
	if err != nil {
		t.Fatalf("TopByField model: %v", err)
	}
	if len(top) != 3 || top[0].Label != "gpt-4o" {
		t.Fatalf("TopByField model: %+v", top)
	}
	// channel_id numeric labels.
	topC, err := d.TopByField(QueryFilter{}, "channel_id", 5, nil)
	if err != nil {
		t.Fatalf("TopByField channel: %v", err)
	}
	if len(topC) != 3 || topC[0].Label == "" {
		t.Fatalf("TopByField channel: %+v", topC)
	}

	// ListFiles: distinct dates.
	files, err := d.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("ListFiles empty")
	}

	// DeleteFiles removes whole days. Deleting today's date removes
	// all rows (all were created within the same UTC day).
	day := files[0]
	if err := d.DeleteFiles([]string{day}); err != nil {
		t.Fatalf("DeleteFiles: %v", err)
	}
	if _, total, _ := d.QueryAcross(QueryFilter{Limit: 10}, nil); total != 0 {
		t.Fatalf("rows after DeleteFiles: %d", total)
	}
	if _, err := d.ListFiles(); err != nil {
		t.Fatalf("ListFiles after delete: %v", err)
	}
}

// TestDayRangeConds verifies day-identifier parsing including the
// rollover "-N" suffix.
func TestDayRangeConds(t *testing.T) {
	conds, args := dayRangeConds([]string{"2026-08-02", "2026-07-30-1"})
	if conds == "" {
		t.Fatal("no conditions built")
	}
	if len(args) != 4 {
		t.Fatalf("args=%d, want 4", len(args))
	}
	start, _ := time.Parse("2006-01-02", "2026-08-02")
	if args[0] != start.UTC().Unix() {
		t.Fatalf("args[0]=%v, want %v", args[0], start.Unix())
	}
	if args[1] != start.UTC().Unix()+86400 {
		t.Fatalf("args[1]=%v", args[1])
	}
	// Garbage days are skipped.
	if conds2, args2 := dayRangeConds([]string{"not-a-day"}); conds2 != "" || args2 != nil {
		t.Fatalf("garbage days: %q %v", conds2, args2)
	}
}
