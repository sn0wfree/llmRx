package logstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// Meta v2 (P21): the rollup must carry the token split and per-group
// error counts the monitoring page reads.
func TestJSONL_MetaV2TokenSplit(t *testing.T) {
	d, dir := newJSONLDriver(t)
	base := time.Date(2027, 5, 1, 10, 0, 0, 0, time.UTC)
	e := jsonlLog(1, 7, "m", 200, base)
	e.PromptTokens = 100
	e.CompletionTokens = 50
	e.CachedTokens = 40
	e.ReasoningTokens = 20
	e.RealCostUSD = 0.5
	e.BilledCostUSD = 0.6
	if err := d.Insert(e); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	bad := jsonlLog(2, 7, "m", 500, base.Add(time.Minute))
	bad.PromptTokens = 10
	bad.CompletionTokens = 5
	if err := d.Insert(bad); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "2027-05-01"+metaExt))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var m fileMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal meta: %v", err)
	}
	if m.MetaVersion != metaVersion {
		t.Fatalf("meta version = %d, want %d", m.MetaVersion, metaVersion)
	}
	g := m.ByChannel["7"]
	if g == nil {
		t.Fatalf("by_channel missing channel 7: %v", m.ByChannel)
	}
	if g.Prompt != 110 || g.Completion != 55 || g.Cached != 40 || g.Reasoning != 20 {
		t.Fatalf("group split: %+v", g)
	}
	if g.Errors != 1 {
		t.Fatalf("group errors = %d, want 1", g.Errors)
	}
	if g.Tokens != 165 {
		t.Fatalf("group tokens = %d, want 165", g.Tokens)
	}
}

// Pre-v2 rollups (no meta_version field) must be treated as missing so
// they rebuild once with the v2 fields.
func TestJSONL_MetaV1TriggersRebuild(t *testing.T) {
	d, dir := newJSONLDriver(t)
	base := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	e := jsonlLog(1, 3, "m", 200, base)
	e.PromptTokens = 100
	e.CompletionTokens = 50
	e.CachedTokens = 40
	if err := d.Insert(e); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Downgrade the on-disk meta to a v1-shaped rollup.
	v1 := map[string]any{
		"rows": 1, "prompt_tokens": 100, "completion_tokens": 50,
		"real_cost_usd": e.RealCostUSD, "billed_cost_usd": e.BilledCostUSD,
		"errors": 0,
		"by_channel": map[string]any{
			"3": map[string]any{"count": 1, "tokens": 150, "cost": e.BilledCostUSD, "real_cost": e.RealCostUSD},
		},
	}
	b, err := json.Marshal(v1)
	if err != nil {
		t.Fatalf("marshal v1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2027-06-01"+metaExt), b, 0o644); err != nil {
		t.Fatalf("write v1 meta: %v", err)
	}

	stats, err := d.dailyChannelStats([]string{"2027-06-01"})
	if err != nil {
		t.Fatalf("dailyChannelStats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("stats rows = %d, want 1", len(stats))
	}
	s := stats[0]
	if s.ChannelID != 3 || s.PromptTokens != 100 || s.CompletionTokens != 50 || s.CachedTokens != 40 {
		t.Fatalf("rebuilt stat row: %+v", s)
	}
}

// ScanSince streams only rows at or after the boundary, in ascending
// day-file order, and fn's false stops the stream.
func TestJSONL_ScanSince(t *testing.T) {
	d, _ := newJSONLDriver(t)
	day1 := time.Date(2027, 7, 1, 0, 0, 0, 0, time.UTC)
	day2 := day1.AddDate(0, 0, 1)
	rows := []*model.Log{
		{ChannelID: 1, PromptTokens: 1, CreatedAt: day1.Add(time.Hour)},
		{ChannelID: 1, PromptTokens: 2, CreatedAt: day2.Add(2 * time.Hour)},
		{ChannelID: 1, PromptTokens: 3, CreatedAt: day2.Add(3 * time.Hour)},
	}
	for _, e := range rows {
		if err := d.Insert(e); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	boundary := day2.Add(time.Hour)
	var got []int
	err := d.ScanSince(boundary, func(e *model.Log) bool {
		got = append(got, e.PromptTokens)
		return true
	})
	if err != nil {
		t.Fatalf("ScanSince: %v", err)
	}
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("ScanSince rows = %v, want [2 3]", got)
	}

	// Early stop.
	got = nil
	err = d.ScanSince(boundary, func(e *model.Log) bool {
		got = append(got, e.PromptTokens)
		return false
	})
	if err != nil {
		t.Fatalf("ScanSince early stop: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("early stop saw %d rows, want 1", len(got))
	}
}

// ScanSince filters per row inside the boundary day's file: rows
// before `since` in the same day file are excluded.
func TestJSONL_ScanSinceBoundaryDay(t *testing.T) {
	d, _ := newJSONLDriver(t)
	day1 := time.Date(2027, 8, 1, 0, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{
		day1.Add(1 * time.Hour),
		day1.Add(3 * time.Hour),
		day1.Add(5 * time.Hour),
	} {
		if err := d.Insert(jsonlLog(1, 1, "m", 200, at)); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	var got []int
	err := d.ScanSince(day1.Add(2*time.Hour), func(e *model.Log) bool {
		got = append(got, e.CreatedAt.Hour())
		return true
	})
	if err != nil {
		t.Fatalf("ScanSince: %v", err)
	}
	if len(got) != 2 || got[0] != 3 || got[1] != 5 {
		t.Fatalf("boundary-day rows = %v, want hours [3 5]", got)
	}
}
