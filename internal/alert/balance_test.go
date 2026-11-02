package alert

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

// balance_low thresholds live on the channel (each has its own
// currency), so these tests build channels with explicit snapshots and
// thresholds rather than driving the log store.

func newBalanceStore(t *testing.T) *store.SQLite {
	t.Helper()
	s, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mkChannel(t *testing.T, st *store.SQLite, name, balance string, threshold float64, status model.ChannelStatus) {
	t.Helper()
	err := st.CreateChannel(&model.Channel{
		Name: name, Provider: "deepseek", Protocol: "openai",
		BaseURL: "https://" + name + ".example", Status: status,
		Balance: balance, BalanceCurrency: "CNY", BalanceThreshold: threshold,
	})
	if err != nil {
		t.Fatalf("CreateChannel %s: %v", name, err)
	}
}

func balanceAlert(threshold float64) *model.Alert {
	return &model.Alert{
		Name:      "low-balance",
		Type:      model.AlertBalanceLow,
		Threshold: threshold,
		WindowSec: 300,
		Enabled:   true,
	}
}

func TestEvalBalanceLow_FiresOnLowChannel(t *testing.T) {
	st := newBalanceStore(t)
	mkChannel(t, st, "low", "5", 10, model.ChannelEnabled)
	mkChannel(t, st, "healthy", "100", 10, model.ChannelEnabled)
	mkChannel(t, st, "equal", "10", 10, model.ChannelEnabled)
	mkChannel(t, st, "unmonitored", "1", 0, model.ChannelEnabled)
	mkChannel(t, st, "unqueried", "", 10, model.ChannelEnabled)
	mkChannel(t, st, "disabled-low", "1", 10, model.ChannelDisabled)

	fired, payload, err := Evaluate(balanceAlert(0), time.Now(), st, nil)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fired {
		t.Fatal("expected the rule to fire")
	}
	low, ok := payload["low_channels"].([]map[string]any)
	if !ok || len(low) != 1 {
		t.Fatalf("low_channels = %#v, want exactly the 'low' channel", payload["low_channels"])
	}
	if low[0]["name"] != "low" {
		t.Fatalf("wrong channel reported: %v", low[0])
	}
	// The payload must carry the currency: the threshold is meaningless
	// without it.
	if low[0]["currency"] != "CNY" {
		t.Fatalf("currency missing from payload: %v", low[0])
	}
	if low[0]["balance"] != "5" || low[0]["threshold"] != float64(10) {
		t.Fatalf("payload values wrong: %v", low[0])
	}
}

// Exact equality does not fire: "below the line" is strict.
func TestEvalBalanceLow_StrictlyBelow(t *testing.T) {
	st := newBalanceStore(t)
	mkChannel(t, st, "exact", "10", 10, model.ChannelEnabled)

	fired, _, err := Evaluate(balanceAlert(0), time.Now(), st, nil)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fired {
		t.Fatal("balance equal to threshold must not fire")
	}
}

// The rule's threshold is a count of low channels.
func TestEvalBalanceLow_RequiredCount(t *testing.T) {
	st := newBalanceStore(t)
	mkChannel(t, st, "low", "5", 10, model.ChannelEnabled)

	if fired, _, _ := Evaluate(balanceAlert(2), time.Now(), st, nil); fired {
		t.Fatal("one low channel must not satisfy a count of 2")
	}
	if fired, _, _ := Evaluate(balanceAlert(1), time.Now(), st, nil); !fired {
		t.Fatal("one low channel must satisfy a count of 1")
	}
}

// Unknown balances are not low: "never queried" and "query failed" are
// different problems with a different fix.
func TestEvalBalanceLow_IgnoresUnknownBalance(t *testing.T) {
	st := newBalanceStore(t)
	mkChannel(t, st, "unqueried", "", 10, model.ChannelEnabled)

	fired, _, err := Evaluate(balanceAlert(0), time.Now(), st, nil)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fired {
		t.Fatal("an unqueried balance must not count as low")
	}
}

// A stored error with an old value still evaluates the old value —
// losing a last-known number would be worse than a stale alarm.
func TestEvalBalanceLow_UsesStaleValueOnError(t *testing.T) {
	st := newBalanceStore(t)
	ch := &model.Channel{
		Name: "stale", Provider: "deepseek", Protocol: "openai",
		BaseURL: "https://stale.example", Status: model.ChannelEnabled,
		Balance: "3", BalanceCurrency: "CNY", BalanceThreshold: 10,
	}
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if err := st.UpdateChannelBalance(ch.ID, "", "", time.Now(), "upstream 503"); err != nil {
		t.Fatalf("UpdateChannelBalance: %v", err)
	}

	fired, payload, err := Evaluate(balanceAlert(0), time.Now(), st, nil)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fired {
		t.Fatal("the last known balance is below the line; it must still fire")
	}
	low := payload["low_channels"].([]map[string]any)
	if low[0]["balance"] != "3" {
		t.Fatalf("stale value lost: %v", low[0])
	}
}

func TestEvaluate_UnknownTypeStillErrors(t *testing.T) {
	st := newBalanceStore(t)
	_, _, err := Evaluate(&model.Alert{Type: "nope"}, time.Now(), st, nil)
	if err == nil {
		t.Fatal("unknown alert type must return an error")
	}
}
