package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// TestChannelUsageProbeRoundTrip covers the P22 columns: the probe
// config and the cached report must survive a write/read cycle, and
// the cookie must never come back through a JSON decode.
func TestChannelUsageProbeRoundTrip(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()

	ch := &model.Channel{
		Name: "probe-ch", Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1",
		Models: []string{"m"}, Status: model.ChannelEnabled,
		UsageProbe: model.UsageProbe{
			Kind: model.UsageProbeZhipuWeb,
			// CookieCipher is json:"-" — simulate what the webui sealer
			// writes by setting the field directly on the model.
			CookieCipher: "sealed-cookie-blob",
			CookieMasked: "sid=***abcd",
			EndpointURL:  "https://example.test/usage",
			IntervalSec:  1800,
		},
	}
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := st.GetChannel(ch.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.UsageProbe.Kind != model.UsageProbeZhipuWeb ||
		got.UsageProbe.CookieMasked != "sid=***abcd" ||
		got.UsageProbe.IntervalSec != 1800 ||
		got.UsageProbe.EndpointURL != "https://example.test/usage" {
		t.Fatalf("probe round-trip = %+v", got.UsageProbe)
	}

	// Update: change the kind and clear the endpoint.
	got.UsageProbe = model.UsageProbe{Kind: model.UsageProbeOpenRouter}
	if err := st.UpdateChannel(got); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, err := st.GetChannel(ch.ID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if again.UsageProbe.Kind != model.UsageProbeOpenRouter || again.UsageProbe.EndpointURL != "" {
		t.Fatalf("updated probe = %+v", again.UsageProbe)
	}
}

// TestUpdateChannelUsageState covers the P14-style "keep the last good
// value on failure" contract for the P22 state columns.
func TestUpdateChannelUsageState(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()

	ch := &model.Channel{
		Name: "c", Provider: "p", BaseURL: "https://x", Models: []string{"m"},
		Status: model.ChannelEnabled,
	}
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("create: %v", err)
	}

	t1 := time.Now().UTC().Truncate(time.Second)
	if err := st.UpdateChannelUsageState(ch.ID, `{"source":"openrouter_key"}`, t1, ""); err != nil {
		t.Fatalf("state write: %v", err)
	}
	got, err := st.GetChannel(ch.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.UsageState != `{"source":"openrouter_key"}` || got.UsageCheckedAt == nil {
		t.Fatalf("state = %q checked=%v", got.UsageState, got.UsageCheckedAt)
	}

	// A failure records the error and keeps the state.
	t2 := t1.Add(time.Minute)
	if err := st.UpdateChannelUsageState(ch.ID, "", t2, "upstream 500"); err != nil {
		t.Fatalf("error write: %v", err)
	}
	got, _ = st.GetChannel(ch.ID)
	if got.UsageState != `{"source":"openrouter_key"}` {
		t.Fatalf("last good state lost: %q", got.UsageState)
	}
	if got.UsageError != "upstream 500" {
		t.Fatalf("error = %q", got.UsageError)
	}

	// A later success clears the error.
	if err := st.UpdateChannelUsageState(ch.ID, `{"source":"x"}`, t2.Add(time.Minute), ""); err != nil {
		t.Fatalf("recovery write: %v", err)
	}
	got, _ = st.GetChannel(ch.ID)
	if got.UsageError != "" || got.UsageState != `{"source":"x"}` {
		t.Fatalf("after recovery = %q / %q", got.UsageState, got.UsageError)
	}
}

// TestUsagePointsHistory covers append/read/prune.
func TestUsagePointsHistory(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()

	ch := &model.Channel{
		Name: "c", Provider: "p", BaseURL: "https://x", Models: []string{"m"},
		Status: model.ChannelEnabled,
	}
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("create: %v", err)
	}

	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	resets := base.Add(5 * time.Hour)
	for i := 0; i < 3; i++ {
		if err := st.AppendUsagePoint(UsagePoint{
			ChannelID: ch.ID,
			TS:        base.Add(time.Duration(i) * time.Minute),
			Source:    "openrouter_key",
			Window:    "weekly",
			Used:      float64(i) * 10,
			LimitVal:  100,
			Remaining: 100 - float64(i)*10,
			Unit:      "usd",
			ResetsAt:  resets,
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	pts, err := st.UsagePoints(ch.ID, base.Add(-time.Minute), 100)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(pts) != 3 {
		t.Fatalf("points = %d, want 3", len(pts))
	}
	if pts[0].Window != "weekly" || pts[0].LimitVal != 100 || pts[0].Unit != "usd" {
		t.Fatalf("first point = %+v", pts[0])
	}
	if !pts[0].ResetsAt.Equal(resets) {
		t.Fatalf("resets_at = %v, want %v", pts[0].ResetsAt, resets)
	}

	// Prune everything older than the newest point.
	n, err := st.PruneUsagePoints(base.Add(90 * time.Second))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 2 {
		t.Fatalf("pruned %d rows, want 2", n)
	}
	pts, _ = st.UsagePoints(ch.ID, base.Add(-time.Minute), 100)
	if len(pts) != 1 {
		t.Fatalf("after prune = %d points", len(pts))
	}
}
