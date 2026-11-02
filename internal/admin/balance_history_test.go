package admin_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// The history endpoint is a pure read over channel_balances; these
// tests cover the wire shape the trend chart depends on.

func TestBalanceHistory_Empty(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("ds", "deepseek", "https://api.deepseek.com/v1", []string{"deepseek-chat"}, "sk-a")

	rec := do(t, app.Admin.Routes(), http.MethodGet, "/channels/1/balance-history", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ChannelID int64            `json:"channel_id"`
		Name      string           `json:"name"`
		Days      int              `json:"days"`
		Points    []map[string]any `json:"points"`
	}
	decodeJSON(t, rec, &resp)

	if resp.ChannelID != 1 || resp.Days != 30 {
		t.Fatalf("resp = %+v", resp)
	}
	// Empty must be [] not null: the chart iterates it directly.
	if resp.Points == nil {
		t.Fatal("points must be an empty array, not null")
	}
	if len(resp.Points) != 0 {
		t.Fatalf("points = %v", resp.Points)
	}
}

func TestBalanceHistory_ReturnsPoints(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("ds", "deepseek", "https://api.deepseek.com/v1", []string{"deepseek-chat"}, "sk-a")

	now := time.Now().UTC()
	for _, p := range []struct {
		ago  time.Duration
		bal  string
	}{
		{48 * time.Hour, "150"},
		{24 * time.Hour, "130"},
		{1 * time.Hour, "110"},
	} {
		if err := app.Store.AppendBalancePoint(1, now.Add(-p.ago), p.bal, "CNY"); err != nil {
			t.Fatalf("AppendBalancePoint: %v", err)
		}
	}

	rec := do(t, app.Admin.Routes(), http.MethodGet, "/channels/1/balance-history?days=7", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Days int `json:"days"`
		Points []struct {
			Balance string `json:"balance"`
			TS      string `json:"ts"`
		} `json:"points"`
	}
	decodeJSON(t, rec, &resp)

	if resp.Days != 7 {
		t.Fatalf("days = %d, want 7", resp.Days)
	}
	if len(resp.Points) != 3 {
		t.Fatalf("points = %d, want 3", len(resp.Points))
	}
	// Oldest first: the chart draws left-to-right in time.
	if resp.Points[0].Balance != "150" || resp.Points[2].Balance != "110" {
		t.Fatalf("points not oldest-first: %+v", resp.Points)
	}
}

// A window that excludes the points must return none of them.
func TestBalanceHistory_WindowFilter(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("ds", "deepseek", "https://api.deepseek.com/v1", []string{"deepseek-chat"}, "sk-a")

	now := time.Now().UTC()
	if err := app.Store.AppendBalancePoint(1, now.AddDate(0, 0, -40), "999", "CNY"); err != nil {
		t.Fatalf("append: %v", err)
	}

	rec := do(t, app.Admin.Routes(), http.MethodGet, "/channels/1/balance-history?days=30", sess, "")
	var resp struct {
		Points []map[string]any `json:"points"`
	}
	decodeJSON(t, rec, &resp)
	if len(resp.Points) != 0 {
		t.Fatalf("a 40-day-old point leaked into a 30-day window: %v", resp.Points)
	}
}

func TestBalanceHistory_NotFound(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	rec := do(t, app.Admin.Routes(), http.MethodGet, "/channels/999/balance-history", sess, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// An absurd days value is clamped rather than trusted.
func TestBalanceHistory_ClampsDays(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("ds", "deepseek", "https://api.deepseek.com/v1", []string{"deepseek-chat"}, "sk-a")

	for _, q := range []string{"?days=0", "?days=-5", "?days=99999", "?days=abc"} {
		rec := do(t, app.Admin.Routes(), http.MethodGet, "/channels/1/balance-history"+q, sess, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", q, rec.Code)
		}
		var resp struct {
			Days int `json:"days"`
		}
		decodeJSON(t, rec, &resp)
		if resp.Days != 30 {
			t.Fatalf("%s: days = %d, want the 30-day default", q, resp.Days)
		}
	}
}

func TestBalanceHistory_RequiresSession(t *testing.T) {
	app := testhelper.New(t)
	if rec := do(t, app.Admin.Routes(), http.MethodGet, "/channels/1/balance-history", "", ""); rec.Code == http.StatusOK {
		t.Fatal("unauthenticated request succeeded")
	}
}
