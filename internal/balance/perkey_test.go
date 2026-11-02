package balance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/store"
)

// Balance is account-level, so a channel with several keys has several
// balances. These tests pin the per-key behaviour and its aggregation.

func newMultiKeyMonitor(t *testing.T, st *store.SQLite, ch *model.Channel, keys []model.Key) *Monitor {
	t.Helper()
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	for i := range keys {
		keys[i].ChannelID = ch.ID
		if keys[i].KeyMasked == "" {
			keys[i].KeyMasked = "sk-a***mnop"
		}
		if err := st.CreateKey(&keys[i]); err != nil {
			t.Fatalf("CreateKey %d: %v", i, err)
		}
	}
	m, _, _ := newMonitor(t, []*model.Channel{ch})
	m.store = st
	m.SetKeyLister(st)
	return m
}

// perKeyQuery returns a distinct balance per key value, so the minimum
// is unambiguous.
func perKeyQuery(byKey map[string]float64) func(context.Context, string, string, string, string) (provider.Balance, error) {
	return func(_ context.Context, _ string, apiKey, _, _ string) (provider.Balance, error) {
		v, ok := byKey[apiKey]
		if !ok {
			return provider.Balance{}, errors.New("boom")
		}
		return provider.Balance{Available: v, Currency: "CNY"}, nil
	}
}

// A single-key channel must behave exactly as before: one query, the
// channel row gets the value.
func TestPerKey_SingleKeyUnchanged(t *testing.T) {
	st := newHistoryStore(t)
	ch := deepseekChannel(1)
	m := newMultiKeyMonitor(t, st, ch, []model.Key{{Key: "sk-one", Status: model.KeyActive}})
	m.SetQuery(perKeyQuery(map[string]float64{"sk-one": 42}))

	if _, err := m.QueryChannel(context.Background(), ch); err != nil {
		t.Fatalf("QueryChannel: %v", err)
	}

	got, _ := st.GetChannel(ch.ID)
	if got.Balance != "42" {
		t.Fatalf("channel balance = %q, want 42", got.Balance)
	}
	keys, _ := st.GetKeys(ch.ID)
	if len(keys) != 1 || keys[0].Balance != "42" {
		t.Fatalf("key balance not written: %+v", keys)
	}
}

// Every active key is queried, and the channel reports the MINIMUM —
// the number that answers "when will this channel run out".
func TestPerKey_QueriesAllAndTakesMin(t *testing.T) {
	st := newHistoryStore(t)
	ch := deepseekChannel(1)
	m := newMultiKeyMonitor(t, st, ch, []model.Key{
		{Key: "sk-a", Status: model.KeyActive},
		{Key: "sk-b", Status: model.KeyActive},
		{Key: "sk-c", Status: model.KeyActive},
	})
	m.SetQuery(perKeyQuery(map[string]float64{
		"sk-a": 100, "sk-b": 7.5, "sk-c": 60,
	}))

	if _, err := m.QueryChannel(context.Background(), ch); err != nil {
		t.Fatalf("QueryChannel: %v", err)
	}

	keys, _ := st.GetKeys(ch.ID)
	byMasked := map[string]string{}
	for _, k := range keys {
		byMasked[k.Key] = k.Balance
	}
	if byMasked["sk-a"] != "100" || byMasked["sk-b"] != "7.5" || byMasked["sk-c"] != "60" {
		t.Fatalf("per-key balances = %+v", byMasked)
	}

	got, _ := st.GetChannel(ch.ID)
	if got.Balance != "7.5" {
		t.Fatalf("channel balance = %q, want the minimum 7.5 (not a sum)", got.Balance)
	}
}

// One key failing must not stop the others, and the channel still takes
// the minimum of the successful ones.
func TestPerKey_OneFailureDoesNotStopOthers(t *testing.T) {
	st := newHistoryStore(t)
	ch := deepseekChannel(1)
	m := newMultiKeyMonitor(t, st, ch, []model.Key{
		{Key: "sk-good-1", Status: model.KeyActive},
		{Key: "sk-bad", Status: model.KeyActive},
		{Key: "sk-good-2", Status: model.KeyActive},
	})
	m.SetQuery(perKeyQuery(map[string]float64{
		"sk-good-1": 80, "sk-good-2": 30,
	}))

	if _, err := m.QueryChannel(context.Background(), ch); err != nil {
		t.Fatalf("QueryChannel must succeed when some keys work: %v", err)
	}

	keys, _ := st.GetKeys(ch.ID)
	var sawError bool
	for _, k := range keys {
		if k.Key == "sk-bad" && k.BalanceError != "" {
			sawError = true
		}
		if k.Key == "sk-bad" && k.Balance != "" {
			t.Fatalf("failed key must not carry a balance: %+v", k)
		}
	}
	if !sawError {
		t.Fatal("the failing key's error was not recorded")
	}

	got, _ := st.GetChannel(ch.ID)
	if got.Balance != "30" {
		t.Fatalf("channel balance = %q, want 30 (min of the successful keys)", got.Balance)
	}
}

// When every key fails, the channel keeps its previous value and records
// an error — losing the last known number is worse than a stale one.
func TestPerKey_AllFailKeepsPrevious(t *testing.T) {
	st := newHistoryStore(t)
	ch := deepseekChannel(1)
	ch.Balance = "55"
	ch.BalanceCurrency = "CNY"
	m := newMultiKeyMonitor(t, st, ch, []model.Key{
		{Key: "sk-1", Status: model.KeyActive},
		{Key: "sk-2", Status: model.KeyActive},
	})
	m.SetQuery(perKeyQuery(map[string]float64{})) // every key errors

	if _, err := m.QueryChannel(context.Background(), ch); err == nil {
		t.Fatal("expected an error when every key fails")
	}

	got, _ := st.GetChannel(ch.ID)
	if got.Balance != "55" {
		t.Fatalf("channel balance = %q, want the previous 55 kept", got.Balance)
	}
	if got.BalanceError == "" {
		t.Fatal("error not recorded on the channel")
	}
}

// A non-active key is skipped and its balance cleared: a stale value on
// a key that will never be used again reads as if that account were
// still in play.
func TestPerKey_InactiveKeySkippedAndCleared(t *testing.T) {
	st := newHistoryStore(t)
	ch := deepseekChannel(1)
	m := newMultiKeyMonitor(t, st, ch, []model.Key{
		{Key: "sk-active", Status: model.KeyActive},
		{Key: "sk-disabled", Status: model.KeyDisabled},
	})

	// Seed a stale balance on the disabled key.
	keys, _ := st.GetKeys(ch.ID)
	var disabledID int64
	for _, k := range keys {
		if k.Key == "sk-disabled" {
			disabledID = k.ID
		}
	}
	if err := st.UpdateKeyBalance(disabledID, "999", "CNY", time.Now().UTC(), ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	m.SetQuery(perKeyQuery(map[string]float64{"sk-active": 12}))
	if _, err := m.QueryChannel(context.Background(), ch); err != nil {
		t.Fatalf("QueryChannel: %v", err)
	}

	keys, _ = st.GetKeys(ch.ID)
	for _, k := range keys {
		if k.Key == "sk-disabled" {
			if k.Balance != "" {
				t.Fatalf("disabled key kept a stale balance: %q", k.Balance)
			}
			if k.BalanceUpdatedAt != nil {
				t.Fatal("disabled key kept an update time")
			}
		}
	}
	got, _ := st.GetChannel(ch.ID)
	if got.Balance != "12" {
		t.Fatalf("channel balance = %q, want 12 (only the active key counts)", got.Balance)
	}
}

// The channel's history records the minimum, so the trend reflects the
// channel's usable runway.
func TestPerKey_HistoryRecordsMinimum(t *testing.T) {
	st := newHistoryStore(t)
	ch := deepseekChannel(1)
	m := newMultiKeyMonitor(t, st, ch, []model.Key{
		{Key: "sk-x", Status: model.KeyActive},
		{Key: "sk-y", Status: model.KeyActive},
	})
	m.SetHistoryStore(st)
	m.SetQuery(perKeyQuery(map[string]float64{"sk-x": 44, "sk-y": 11}))

	if _, err := m.QueryChannel(context.Background(), ch); err != nil {
		t.Fatalf("QueryChannel: %v", err)
	}

	points, _ := st.BalanceHistory(ch.ID, time.Now().Add(-time.Hour), 10)
	if len(points) != 1 {
		t.Fatalf("points = %d, want 1", len(points))
	}
	if points[0].Balance != "11" {
		t.Fatalf("history point = %q, want the minimum 11", points[0].Balance)
	}
}

// Without a key lister the monitor falls back to the pool's pick, which
// is the pre-phase-4 behaviour (covered here so a future refactor cannot
// silently drop the fallback).
func TestPerKey_WithoutListerFallsBack(t *testing.T) {
	ch := deepseekChannel(1)
	m, fs, _ := newMonitor(t, []*model.Channel{ch})
	m.SetQuery(func(context.Context, string, string, string, string) (provider.Balance, error) {
		return provider.Balance{Available: 3, Currency: "CNY"}, nil
	})

	if _, err := m.QueryChannel(context.Background(), ch); err != nil {
		t.Fatalf("QueryChannel: %v", err)
	}
	if call, ok := fs.last(); !ok || call.balance != "3" {
		t.Fatalf("channel snapshot not written: %+v", call)
	}
	// The fallback path also writes the key-level row (the picked key).
	if _, ok := fs.lastKey(); !ok {
		t.Fatal("key-level balance not written on the fallback path")
	}
}

func TestKeyBalance_RoundTrip(t *testing.T) {
	st := newHistoryStore(t)
	ch := deepseekChannel(1)
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	k := &model.Key{ChannelID: ch.ID, Key: "sk-round", Status: model.KeyActive, KeyMasked: "sk-r***ound"}
	if err := st.CreateKey(k); err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	// Precision must survive: 12590.0042 must not become 12590.
	if err := st.UpdateKeyBalance(k.ID, "12590.0042", "CNY", now, ""); err != nil {
		t.Fatalf("UpdateKeyBalance: %v", err)
	}

	keys, _ := st.GetKeys(ch.ID)
	if len(keys) != 1 {
		t.Fatalf("keys = %d", len(keys))
	}
	if keys[0].Balance != "12590.0042" || keys[0].BalanceCurrency != "CNY" {
		t.Fatalf("key = %+v", keys[0])
	}
	if keys[0].BalanceUpdatedAt == nil {
		t.Fatal("updated_at not persisted")
	}

	// A failure records the error and leaves the value.
	if err := st.UpdateKeyBalance(k.ID, "", "", now, "upstream 503"); err != nil {
		t.Fatalf("UpdateKeyBalance err path: %v", err)
	}
	keys, _ = st.GetKeys(ch.ID)
	if keys[0].Balance != "12590.0042" {
		t.Fatalf("value lost on failure: %q", keys[0].Balance)
	}
	if keys[0].BalanceError == "" {
		t.Fatal("error not recorded")
	}

	// Clearing resets everything.
	if err := st.ClearKeyBalance(k.ID); err != nil {
		t.Fatalf("ClearKeyBalance: %v", err)
	}
	keys, _ = st.GetKeys(ch.ID)
	if keys[0].Balance != "" || keys[0].BalanceError != "" || keys[0].BalanceUpdatedAt != nil {
		t.Fatalf("clear did not reset: %+v", keys[0])
	}
}
