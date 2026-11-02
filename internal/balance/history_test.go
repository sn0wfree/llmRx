package balance

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/store"
)

func newHistoryStore(t *testing.T) *store.SQLite {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "hist.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newMonitorWithHistory(t *testing.T, st *store.SQLite, ch *model.Channel) *Monitor {
	t.Helper()
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	m, _, _ := newMonitor(t, []*model.Channel{ch})
	m.store = st
	m.SetHistoryStore(st)
	return m
}

// A successful query must append exactly one history point alongside the
// live snapshot.
func TestHistory_SuccessAppendsPoint(t *testing.T) {
	st := newHistoryStore(t)
	ch := deepseekChannel(1)
	m := newMonitorWithHistory(t, st, ch)
	m.SetQuery(func(context.Context, string, string, string, string) (provider.Balance, error) {
		return provider.Balance{Available: 42.5, Currency: "CNY"}, nil
	})

	if _, err := m.QueryChannel(context.Background(), ch); err != nil {
		t.Fatalf("QueryChannel: %v", err)
	}

	points, err := st.BalanceHistory(ch.ID, time.Now().Add(-time.Hour), 100)
	if err != nil {
		t.Fatalf("BalanceHistory: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("points = %d, want 1", len(points))
	}
	if points[0].Balance != "42.5" {
		t.Fatalf("balance = %q, want 42.5", points[0].Balance)
	}
	if points[0].Currency != "CNY" {
		t.Fatalf("currency = %q", points[0].Currency)
	}

	// The live snapshot and the history point must agree byte for byte.
	got, err := st.GetChannel(ch.ID)
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if got.Balance != points[0].Balance {
		t.Fatalf("snapshot %q != history %q", got.Balance, points[0].Balance)
	}
}

// A failed query must NOT insert a point: "no data" and "balance is 0"
// are different facts, and mixing them draws a cliff into the chart.
func TestHistory_FailureWritesNothing(t *testing.T) {
	st := newHistoryStore(t)
	ch := deepseekChannel(1)
	m := newMonitorWithHistory(t, st, ch)
	m.SetQuery(func(context.Context, string, string, string, string) (provider.Balance, error) {
		return provider.Balance{}, errors.New("upstream 503")
	})

	if _, err := m.QueryChannel(context.Background(), ch); err == nil {
		t.Fatal("expected an error")
	}

	points, err := st.BalanceHistory(ch.ID, time.Now().Add(-time.Hour), 100)
	if err != nil {
		t.Fatalf("BalanceHistory: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("points = %d, want 0 (a failed query must not record a point)", len(points))
	}
}

// A manual refresh right after an automatic sweep must not add a second
// point moments later.
func TestHistory_DedupWindow(t *testing.T) {
	st := newHistoryStore(t)
	ch := deepseekChannel(1)
	m := newMonitorWithHistory(t, st, ch)
	m.SetQuery(func(context.Context, string, string, string, string) (provider.Balance, error) {
		return provider.Balance{Available: 10, Currency: "CNY"}, nil
	})

	for i := 0; i < 3; i++ {
		if _, err := m.QueryChannel(context.Background(), ch); err != nil {
			t.Fatalf("QueryChannel %d: %v", i, err)
		}
	}

	points, _ := st.BalanceHistory(ch.ID, time.Now().Add(-time.Hour), 100)
	if len(points) != 1 {
		t.Fatalf("points = %d, want 1 (three rapid refreshes must collapse)", len(points))
	}
}

// Outside the dedup window a new point is written.
func TestHistory_WritesAfterWindow(t *testing.T) {
	st := newHistoryStore(t)
	ch := deepseekChannel(1)
	m := newMonitorWithHistory(t, st, ch)

	// Seed an old point directly.
	old := time.Now().UTC().Add(-historyDedupWindow - time.Minute)
	if err := st.AppendBalancePoint(ch.ID, old, "100", "CNY"); err != nil {
		t.Fatalf("AppendBalancePoint: %v", err)
	}

	m.SetQuery(func(context.Context, string, string, string, string) (provider.Balance, error) {
		return provider.Balance{Available: 90, Currency: "CNY"}, nil
	})
	if _, err := m.QueryChannel(context.Background(), ch); err != nil {
		t.Fatalf("QueryChannel: %v", err)
	}

	points, _ := st.BalanceHistory(ch.ID, time.Now().Add(-48*time.Hour), 100)
	if len(points) != 2 {
		t.Fatalf("points = %d, want 2", len(points))
	}
	if points[0].Balance != "100" || points[1].Balance != "90" {
		t.Fatalf("points not oldest-first: %+v", points)
	}
}

// PruneHistory deletes points outside the window and keeps the rest.
func TestHistory_Prune(t *testing.T) {
	st := newHistoryStore(t)
	ch := deepseekChannel(1)
	m := newMonitorWithHistory(t, st, ch)
	m.SetRetentionDays(func() int { return 30 })

	now := time.Now().UTC()
	if err := st.AppendBalancePoint(ch.ID, now.AddDate(0, 0, -60), "1", "CNY"); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	if err := st.AppendBalancePoint(ch.ID, now.AddDate(0, 0, -5), "2", "CNY"); err != nil {
		t.Fatalf("seed recent: %v", err)
	}

	m.PruneHistory(now)

	points, _ := st.BalanceHistory(ch.ID, now.AddDate(0, 0, -365), 100)
	if len(points) != 1 {
		t.Fatalf("points = %d, want 1 after pruning", len(points))
	}
	if points[0].Balance != "2" {
		t.Fatalf("wrong point survived: %+v", points[0])
	}
}

// Retention days come from a live provider so an admin change applies
// without a restart.
func TestHistory_RetentionLiveUpdate(t *testing.T) {
	m, _, _ := newMonitor(t, nil)
	days := 90
	m.SetRetentionDays(func() int { return days })

	if got := m.historyRetention(); got != 90*24*time.Hour {
		t.Fatalf("retention = %v", got)
	}
	days = 7
	if got := m.historyRetention(); got != 7*24*time.Hour {
		t.Fatalf("retention after update = %v", got)
	}
	// A non-positive value falls back to the default rather than
	// deleting everything.
	days = 0
	if got := m.historyRetention(); got != 90*24*time.Hour {
		t.Fatalf("retention with 0 = %v, want the 90d default", got)
	}
}

// A monitor with no history store must still refresh the live snapshot.
func TestHistory_NoStoreIsHarmless(t *testing.T) {
	ch := deepseekChannel(1)
	m, fs, _ := newMonitor(t, []*model.Channel{ch})
	m.SetQuery(func(context.Context, string, string, string, string) (provider.Balance, error) {
		return provider.Balance{Available: 5, Currency: "CNY"}, nil
	})

	if _, err := m.QueryChannel(context.Background(), ch); err != nil {
		t.Fatalf("QueryChannel: %v", err)
	}
	if call, ok := fs.last(); !ok || call.balance != "5" {
		t.Fatalf("live snapshot not written: %+v", call)
	}
	// Pruning is a no-op.
	m.PruneHistory(time.Now())
}

// History is per channel: one channel's points must not leak into
// another's series.
func TestHistory_PerChannelIsolation(t *testing.T) {
	st := newHistoryStore(t)
	a := deepseekChannel(1)
	a.Name = "a"
	b := deepseekChannel(2)
	b.Name = "b"
	b.BaseURL = "https://other.example"
	for _, ch := range []*model.Channel{a, b} {
		if err := st.CreateChannel(ch); err != nil {
			t.Fatalf("CreateChannel: %v", err)
		}
	}
	now := time.Now().UTC()
	_ = st.AppendBalancePoint(a.ID, now.Add(-time.Hour), "10", "CNY")
	_ = st.AppendBalancePoint(b.ID, now.Add(-time.Hour), "20", "CNY")

	pa, _ := st.BalanceHistory(a.ID, now.Add(-24*time.Hour), 100)
	pb, _ := st.BalanceHistory(b.ID, now.Add(-24*time.Hour), 100)
	if len(pa) != 1 || len(pb) != 1 {
		t.Fatalf("lens = %d, %d", len(pa), len(pb))
	}
	if pa[0].Balance != "10" || pb[0].Balance != "20" {
		t.Fatalf("series crossed: %+v %+v", pa[0], pb[0])
	}
}

// Limit is a hard cap.
func TestHistory_Limit(t *testing.T) {
	st := newHistoryStore(t)
	ch := deepseekChannel(1)
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		if err := st.AppendBalancePoint(ch.ID, now.Add(-time.Duration(i)*time.Minute), "1", ""); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	points, _ := st.BalanceHistory(ch.ID, now.Add(-time.Hour), 3)
	if len(points) != 3 {
		t.Fatalf("points = %d, want 3", len(points))
	}
}

func TestHistory_EmptyReturnsNil(t *testing.T) {
	st := newHistoryStore(t)
	ch := deepseekChannel(1)
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	points, err := st.BalanceHistory(ch.ID, time.Now().Add(-time.Hour), 100)
	if err != nil {
		t.Fatalf("BalanceHistory: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("points = %d, want none", len(points))
	}
}
