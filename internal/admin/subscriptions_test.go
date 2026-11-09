package admin_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/subusage"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// seedProbeChannel creates a channel with a usage probe configured and
// a cached vendor report written through the store (the same path the
// monitor uses).
func seedProbeChannel(t *testing.T, app *testhelper.App, sess, name, reportJSON string) model.Channel {
	t.Helper()
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess, `{
		"name":"`+name+`","provider":"openrouter","base_url":"https://openrouter.ai/api/v1",
		"models":["m1"],
		"usage_probe":{"kind":"openrouter_key"}
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create channel: %d %s", rec.Code, rec.Body.String())
	}
	var ch model.Channel
	decodeJSON(t, rec, &ch)
	if ch.UsageProbe.Kind != model.UsageProbeOpenRouter {
		t.Fatalf("probe not persisted: %+v", ch.UsageProbe)
	}
	if reportJSON != "" {
		if err := app.Store.UpdateChannelUsageState(ch.ID, reportJSON, time.Now(), ""); err != nil {
			t.Fatalf("seed state: %v", err)
		}
	}
	return ch
}

func TestAdmin_ListSubscriptions(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	resets := time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)
	report := `{"source":"openrouter_key","experimental":false,
		"points":[
			{"window":"key_limit","scope":"key","used":80,"limit":100,"remaining":20,"unit":"usd"},
			{"window":"weekly","used":5,"limit":10,"remaining":5,"unit":"usd","resets_at":"` + resets + `"}
		]}`
	ch := seedProbeChannel(t, app, sess, "or-ch", report)
	// A channel without a probe must not appear.
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess,
		`{"name":"plain","provider":"openai","base_url":"https://x","models":["m1"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create plain channel: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, app.Admin.Routes(), http.MethodGet, "/subscriptions", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []struct {
			ChannelID int64  `json:"channel_id"`
			Name      string `json:"name"`
			ProbeKind string `json:"probe_kind"`
			Points    []struct {
				Window    string  `json:"window"`
				Used      float64 `json:"used"`
				Limit     float64 `json:"limit"`
				Ratio     float64 `json:"ratio"`
				Exhausted bool    `json:"exhausted"`
				ResetsAt  string  `json:"resets_at"`
			} `json:"points"`
		} `json:"data"`
	}
	decodeJSON(t, rec, &resp)

	if len(resp.Data) != 1 {
		t.Fatalf("rows = %d, want 1 (unprobed channel must be hidden)", len(resp.Data))
	}
	row := resp.Data[0]
	if row.ChannelID != ch.ID || row.ProbeKind != model.UsageProbeOpenRouter {
		t.Fatalf("row = %+v", row)
	}
	if len(row.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(row.Points))
	}
	if row.Points[0].Window != "key_limit" || row.Points[0].Ratio != 0.8 {
		t.Fatalf("key_limit point = %+v", row.Points[0])
	}
	if row.Points[0].Exhausted {
		t.Fatal("80% must not read as exhausted")
	}
	if row.Points[1].ResetsAt == "" {
		t.Fatal("resets_at lost in the API view")
	}
}

func TestAdmin_RefreshSubscriptionUnconfigured(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	// testhelper does not wire a usage monitor by default, so refresh is
	// a 503 — the channel exists but the probe loop does not.
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess,
		`{"name":"c","provider":"openai","base_url":"https://x","models":["m1"],
		  "usage_probe":{"kind":"openrouter_key"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var ch model.Channel
	decodeJSON(t, rec, &ch)

	rec = do(t, app.Admin.Routes(), http.MethodPost,
		"/subscriptions/"+itoa64(ch.ID)+"/refresh", sess, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("refresh without monitor = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_RefreshSubscriptionNoProbe(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess,
		`{"name":"plain","provider":"openai","base_url":"https://x","models":["m1"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var ch model.Channel
	decodeJSON(t, rec, &ch)
	// 503 wins over 409 here because no monitor is wired; assert that
	// the endpoint exists and fails cleanly rather than 404ing.
	rec = do(t, app.Admin.Routes(), http.MethodPost,
		"/subscriptions/"+itoa64(ch.ID)+"/refresh", sess, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("refresh = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_SubscriptionHistory(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	ch := seedProbeChannel(t, app, sess, "hist-ch", "")

	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		if err := app.Store.AppendUsagePoint(storeUsagePoint(ch.ID, base.Add(time.Duration(i)*time.Minute), 10*float64(i+1))); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	rec := do(t, app.Admin.Routes(), http.MethodGet,
		"/subscriptions/"+itoa64(ch.ID)+"/history?days=7", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("history: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ChannelID int64 `json:"channel_id"`
		Days      int   `json:"days"`
		Data      []struct {
			Window string  `json:"window"`
			Used   float64 `json:"used"`
		} `json:"data"`
	}
	decodeJSON(t, rec, &resp)
	if resp.ChannelID != ch.ID || resp.Days != 7 {
		t.Fatalf("envelope = %+v", resp)
	}
	if len(resp.Data) != 3 {
		t.Fatalf("points = %d, want 3", len(resp.Data))
	}
	if resp.Data[0].Window != "daily" || resp.Data[2].Used != 30 {
		t.Fatalf("point shape = %+v", resp.Data)
	}
}

func TestAdmin_SubscriptionParsers(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/subscription-parsers", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("parsers: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data  []string          `json:"data"`
		Notes map[string]string `json:"notes"`
	}
	decodeJSON(t, rec, &resp)
	want := map[string]bool{"openrouter_key": false, "zhipu_web": false, "custom": false}
	for _, n := range resp.Data {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for n, found := range want {
		if !found {
			t.Fatalf("parser %q missing from %v", n, resp.Data)
		}
		if resp.Notes[n] == "" {
			t.Fatalf("parser %q has no note", n)
		}
	}
}

func TestAdmin_ChannelUsageProbeValidation(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	// custom without an endpoint is rejected.
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess,
		`{"name":"bad","provider":"openai","base_url":"https://x","models":["m1"],
		  "usage_probe":{"kind":"custom"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid probe = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	// An unknown kind is rejected.
	rec = do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess,
		`{"name":"bad2","provider":"openai","base_url":"https://x","models":["m1"],
		  "usage_probe":{"kind":"made_up"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	// A valid custom probe is accepted and echoed.
	rec = do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess,
		`{"name":"good","provider":"openai","base_url":"https://x","models":["m1"],
		  "usage_probe":{"kind":"custom","endpoint_url":"https://api.example/usage",
		                 "json_path":"data.quota","window_name":"monthly","unit":"usd"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid probe = %d: %s", rec.Code, rec.Body.String())
	}
	var ch model.Channel
	decodeJSON(t, rec, &ch)
	if ch.UsageProbe.Kind != model.UsageProbeCustom || ch.UsageProbe.JSONPath != "data.quota" {
		t.Fatalf("echoed probe = %+v", ch.UsageProbe)
	}

	// Clearing it via PATCH stops the probe.
	rec = do(t, app.Admin.Routes(), http.MethodPut, "/channels/"+itoa64(ch.ID), sess,
		`{"usage_probe":{}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear probe: %d %s", rec.Code, rec.Body.String())
	}
	var cleared model.Channel
	decodeJSON(t, rec, &cleared)
	if cleared.UsageProbe.Enabled() {
		t.Fatalf("probe should be cleared: %+v", cleared.UsageProbe)
	}
}

// storeUsagePoint is a small helper so the test reads cleanly.
func storeUsagePoint(chID int64, at time.Time, used float64) store.UsagePoint {
	return store.UsagePoint{
		ChannelID: chID, TS: at, Source: "openrouter_key",
		Window: subusage.WindowDaily, Used: used, LimitVal: 100, Remaining: 100 - used, Unit: subusage.UnitUSD,
	}
}

func itoa64(v int64) string { return fmt.Sprintf("%d", v) }
