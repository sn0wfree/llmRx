package logstore

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// The equivalence tests are the safety net for this whole backend
// swap: both drivers must answer the same question with the same
// numbers. A divergence here silently changes the admin pages, so any
// failure is a blocker, not a flake.

// seedData builds a deterministic, mixed corpus spanning several UTC
// days, models, channels, tokens and statuses.
func seedData() []*model.Log {
	rng := rand.New(rand.NewSource(20261101))
	models := []string{"gpt-4", "claude-3", "deepseek-chat", ""}
	statuses := []int{200, 200, 200, 429, 500}
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	out := make([]*model.Log, 0, 240)
	for i := 0; i < 240; i++ {
		day := i / 60 // four days of 60 rows
		e := &model.Log{
			TokenID:          int64(1 + rng.Intn(3)),
			ChannelID:        int64(1 + rng.Intn(2)),
			Model:            models[rng.Intn(len(models))],
			PromptTokens:     rng.Intn(500),
			CompletionTokens: rng.Intn(300),
			CachedTokens:     rng.Intn(50),
			ReasoningTokens:  rng.Intn(20),
			CacheHit:         rng.Intn(10) == 0,
			RealCostUSD:      0.0001 * float64(rng.Intn(1000)),
			BilledCostUSD:    0.0002 * float64(rng.Intn(1000)),
			DurationMs:       int64(rng.Intn(5000)),
			StatusCode:       statuses[rng.Intn(len(statuses))],
			RouterPath:       "static",
			RequestIP:        "10.0.0.1",
			CreatedAt:        base.Add(time.Duration(day)*24*time.Hour + time.Duration(i%60)*time.Minute),
		}
		if rng.Intn(15) == 0 {
			e.Endpoint = "mcp"
			e.Units = 1
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func newPair(t *testing.T) (*SQLiteDriver, *JSONLDriver) {
	t.Helper()
	sq := NewSQLiteDriver()
	if err := sq.Open(t.TempDir()); err != nil {
		t.Fatalf("sqlite Open: %v", err)
	}
	t.Cleanup(func() { _ = sq.Close() })

	jl := NewJSONLDriver()
	if err := jl.Open(t.TempDir()); err != nil {
		t.Fatalf("jsonl Open: %v", err)
	}
	t.Cleanup(func() { _ = jl.Close() })

	return sq, jl
}

func seedBoth(t *testing.T, sq Driver, jl Driver, data []*model.Log) {
	t.Helper()
	if n, err := sq.BatchInsert(data); err != nil || n != len(data) {
		t.Fatalf("sqlite seed n=%d err=%v", n, err)
	}
	if n, err := jl.BatchInsert(data); err != nil || n != len(data) {
		t.Fatalf("jsonl seed n=%d err=%v", n, err)
	}
}

// queryFilters covers every filter dimension the SQLite buildWhere
// understands, plus combinations.
func queryFilters(data []*model.Log) []struct {
	name   string
	filter QueryFilter
} {
	ts := data[0].CreatedAt.Unix()
	mid := data[len(data)/2].CreatedAt.Unix()
	return []struct {
		name   string
		filter QueryFilter
	}{
		{"all", QueryFilter{Limit: 100}},
		{"token 1", QueryFilter{TokenID: 1, Limit: 100}},
		{"tokens 1,2", QueryFilter{TokenIDs: []int64{1, 2}, Limit: 100}},
		{"channel 2", QueryFilter{ChannelID: 2, Limit: 100}},
		{"model gpt-4", QueryFilter{Model: "gpt-4", Limit: 100}},
		{"model empty", QueryFilter{Model: "", Limit: 100}},
		{"status 429", QueryFilter{StatusCode: 429, Limit: 100}},
		{"cache hit", QueryFilter{CacheHit: true, Limit: 100}},
		{"endpoint mcp", QueryFilter{Endpoint: "mcp", Limit: 100}},
		{"from mid", QueryFilter{CreatedFrom: mid, Limit: 100}},
		{"to mid", QueryFilter{CreatedTo: mid, Limit: 100}},
		{"window", QueryFilter{CreatedFrom: ts, CreatedTo: mid, Limit: 100}},
		{"page 1", QueryFilter{Limit: 10, Offset: 0}},
		{"page 2", QueryFilter{Limit: 10, Offset: 10}},
		{"page deep", QueryFilter{Limit: 25, Offset: 100}},
		{"combined", QueryFilter{TokenID: 1, ChannelID: 1, StatusCode: 200, Limit: 100}},
	}
}

func TestEquivalence_QueryAcross(t *testing.T) {
	data := seedData()
	sq, jl := newPair(t)
	seedBoth(t, sq, jl, data)

	for _, tc := range queryFilters(data) {
		t.Run(tc.name, func(t *testing.T) {
			gotRows, gotTotal, err := sq.QueryAcross(tc.filter, nil)
			if err != nil {
				t.Fatalf("sqlite: %v", err)
			}
			wantRows, wantTotal, err := jl.QueryAcross(tc.filter, nil)
			if err != nil {
				t.Fatalf("jsonl: %v", err)
			}

			if gotTotal != wantTotal {
				t.Fatalf("total mismatch: sqlite=%d jsonl=%d", gotTotal, wantTotal)
			}
			if len(gotRows) != len(wantRows) {
				t.Fatalf("row count mismatch: sqlite=%d jsonl=%d", len(gotRows), len(wantRows))
			}
			// Compare the fields that affect what the admin UI shows.
			for i := range gotRows {
				g, w := gotRows[i], wantRows[i]
				if !g.CreatedAt.Equal(w.CreatedAt) {
					t.Fatalf("row %d created_at: sqlite=%v jsonl=%v", i, g.CreatedAt, w.CreatedAt)
				}
				if g.TokenID != w.TokenID || g.ChannelID != w.ChannelID ||
					g.Model != w.Model || g.StatusCode != w.StatusCode ||
					g.PromptTokens != w.PromptTokens ||
					g.CompletionTokens != w.CompletionTokens ||
					g.CacheHit != w.CacheHit || g.Endpoint != w.Endpoint {
					t.Fatalf("row %d differs:\n sqlite=%+v\n jsonl =%+v", i, g, w)
				}
				if diff := g.RealCostUSD - w.RealCostUSD; diff > 1e-9 || diff < -1e-9 {
					t.Fatalf("row %d real cost: sqlite=%v jsonl=%v", i, g.RealCostUSD, w.RealCostUSD)
				}
			}
		})
	}
}

func TestEquivalence_Ordering(t *testing.T) {
	data := seedData()
	sq, jl := newPair(t)
	seedBoth(t, sq, jl, data)

	sqRows, _, err := sq.QueryAcross(QueryFilter{Limit: 500}, nil)
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	jlRows, _, err := jl.QueryAcross(QueryFilter{Limit: 500}, nil)
	if err != nil {
		t.Fatalf("jsonl: %v", err)
	}
	if len(sqRows) != len(jlRows) {
		t.Fatalf("len: sqlite=%d jsonl=%d", len(sqRows), len(jlRows))
	}
	for i := range sqRows {
		if !sqRows[i].CreatedAt.Equal(jlRows[i].CreatedAt) {
			t.Fatalf("order differs at %d: sqlite=%v jsonl=%v",
				i, sqRows[i].CreatedAt, jlRows[i].CreatedAt)
		}
	}
}

func TestEquivalence_LogStats(t *testing.T) {
	data := seedData()
	sq, jl := newPair(t)
	seedBoth(t, sq, jl, data)

	want, err := sq.LogStats(nil)
	if err != nil {
		t.Fatalf("sqlite stats: %v", err)
	}
	got, err := jl.LogStats(nil)
	if err != nil {
		t.Fatalf("jsonl stats: %v", err)
	}
	if want.Total != got.Total {
		t.Fatalf("Total: sqlite=%d jsonl=%d", want.Total, got.Total)
	}
	if want.Errors != got.Errors {
		t.Fatalf("Errors: sqlite=%d jsonl=%d", want.Errors, got.Errors)
	}
	if want.PromptTokens != got.PromptTokens {
		t.Fatalf("PromptTokens: sqlite=%d jsonl=%d", want.PromptTokens, got.PromptTokens)
	}
	if want.CompletionTokens != got.CompletionTokens {
		t.Fatalf("CompletionTokens: sqlite=%d jsonl=%d", want.CompletionTokens, got.CompletionTokens)
	}
	if d := want.RealCostUSD - got.RealCostUSD; d > 1e-6 || d < -1e-6 {
		t.Fatalf("RealCostUSD: sqlite=%v jsonl=%v", want.RealCostUSD, got.RealCostUSD)
	}
	if d := want.BilledCostUSD - got.BilledCostUSD; d > 1e-6 || d < -1e-6 {
		t.Fatalf("BilledCostUSD: sqlite=%v jsonl=%v", want.BilledCostUSD, got.BilledCostUSD)
	}
}

func TestEquivalence_TimeSeries(t *testing.T) {
	data := seedData()
	sq, jl := newPair(t)
	seedBoth(t, sq, jl, data)

	for _, bucket := range []int64{3600, 86400} {
		t.Run(fmt.Sprintf("bucket%d", bucket), func(t *testing.T) {
			want, err := sq.TimeSeries(QueryFilter{}, bucket, nil)
			if err != nil {
				t.Fatalf("sqlite: %v", err)
			}
			got, err := jl.TimeSeries(QueryFilter{}, bucket, nil)
			if err != nil {
				t.Fatalf("jsonl: %v", err)
			}
			if len(want) != len(got) {
				t.Fatalf("buckets: sqlite=%d jsonl=%d", len(want), len(got))
			}
			for i := range want {
				if want[i].Bucket != got[i].Bucket {
					t.Fatalf("bucket %d key: sqlite=%d jsonl=%d", i, want[i].Bucket, got[i].Bucket)
				}
				if want[i].Requests != got[i].Requests {
					t.Fatalf("bucket %d requests: sqlite=%d jsonl=%d", i, want[i].Requests, got[i].Requests)
				}
				if want[i].Errors != got[i].Errors {
					t.Fatalf("bucket %d errors: sqlite=%d jsonl=%d", i, want[i].Errors, got[i].Errors)
				}
				if want[i].PromptTokens != got[i].PromptTokens ||
					want[i].CompletionTokens != got[i].CompletionTokens {
					t.Fatalf("bucket %d tokens differ", i)
				}
				if d := want[i].RealCostUSD - got[i].RealCostUSD; d > 1e-6 || d < -1e-6 {
					t.Fatalf("bucket %d real cost: sqlite=%v jsonl=%v", i, want[i].RealCostUSD, got[i].RealCostUSD)
				}
			}
		})
	}
}

func TestEquivalence_TopByField(t *testing.T) {
	data := seedData()
	sq, jl := newPair(t)
	seedBoth(t, sq, jl, data)

	filters := []struct {
		name   string
		filter QueryFilter
	}{
		{"unfiltered", QueryFilter{}},
		{"by token", QueryFilter{TokenID: 1}},
		{"by status", QueryFilter{StatusCode: 200}},
	}
	fields := []string{"model", "channel_id", "token_id"}

	for _, f := range filters {
		for _, field := range fields {
			t.Run(f.name+"/"+field, func(t *testing.T) {
				want, err := sq.TopByField(f.filter, field, 20, nil)
				if err != nil {
					t.Fatalf("sqlite: %v", err)
				}
				got, err := jl.TopByField(f.filter, field, 20, nil)
				if err != nil {
					t.Fatalf("jsonl: %v", err)
				}
				if len(want) != len(got) {
					t.Fatalf("groups: sqlite=%d jsonl=%d\nwant=%+v\ngot=%+v", len(want), len(got), want, got)
				}
				// Compare as label→metric maps so tie ordering
				// differences don't create false failures.
				wm := map[string]NamedMetric{}
				for _, m := range want {
					wm[m.Label] = m
				}
				gm := map[string]NamedMetric{}
				for _, m := range got {
					gm[m.Label] = m
				}
				if !reflect.DeepEqual(sortedKeys(wm), sortedKeys(gm)) {
					t.Fatalf("labels differ:\nwant=%v\ngot =%v", sortedKeys(wm), sortedKeys(gm))
				}
				for label, w := range wm {
					g := gm[label]
					if w.Count != g.Count {
						t.Fatalf("%s count: sqlite=%d jsonl=%d", label, w.Count, g.Count)
					}
					if w.Tokens != g.Tokens {
						t.Fatalf("%s tokens: sqlite=%d jsonl=%d", label, w.Tokens, g.Tokens)
					}
					if d := w.Cost - g.Cost; d > 1e-6 || d < -1e-6 {
						t.Fatalf("%s cost: sqlite=%v jsonl=%v", label, w.Cost, g.Cost)
					}
				}
			})
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Empty stores must agree too — the admin dashboard hits these paths
// on a fresh install.
func TestEquivalence_EmptyStores(t *testing.T) {
	sq, jl := newPair(t)

	sqStats, err := sq.LogStats(nil)
	if err != nil {
		t.Fatalf("sqlite stats: %v", err)
	}
	jlStats, err := jl.LogStats(nil)
	if err != nil {
		t.Fatalf("jsonl stats: %v", err)
	}
	if sqStats != jlStats {
		t.Fatalf("stats differ: sqlite=%+v jsonl=%+v", sqStats, jlStats)
	}

	if _, t1, err := sq.QueryAcross(QueryFilter{}, nil); err != nil || t1 != 0 {
		t.Fatalf("sqlite query: total=%d err=%v", t1, err)
	}
	if _, t2, err := jl.QueryAcross(QueryFilter{}, nil); err != nil || t2 != 0 {
		t.Fatalf("jsonl query: total=%d err=%v", t2, err)
	}

	if s1, err := sq.TimeSeries(QueryFilter{}, 3600, nil); err != nil || len(s1) != 0 {
		t.Fatalf("sqlite series: len=%d err=%v", len(s1), err)
	}
	if s2, err := jl.TimeSeries(QueryFilter{}, 3600, nil); err != nil || len(s2) != 0 {
		t.Fatalf("jsonl series: len=%d err=%v", len(s2), err)
	}
}
