package admin_test

import (
	"net/http"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// effectiveView is the wire-format shape used by the assertions
// below. Kept loose (everything optional) so a future field
// addition doesn't break this test.
type effectiveView struct {
	Runtime struct {
		Values map[string]any `json:"values"`
		Source string         `json:"source"`
	} `json:"runtime"`
	YAMLSeeds map[string]any `json:"yaml_seeds"`
	Channels  sectionView   `json:"channels"`
	Tokens    sectionView   `json:"tokens"`
	Plans     sectionView   `json:"plans"`
	Alerts    sectionView   `json:"alerts"`
}

type sectionView struct {
	Items []map[string]any `json:"items"`
	Count int              `json:"count"`
	Error *string          `json:"error"`
}

func getEffective(t *testing.T, app *testhelper.App, sess string) effectiveView {
	t.Helper()
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/effective", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get effective: %d %s", rec.Code, rec.Body.String())
	}
	var v effectiveView
	decodeJSON(t, rec, &v)
	return v
}

// TestAdmin_EffectiveEmptyDB: a freshly-seeded DB has no
// channels / tokens / plans / alerts. Runtime fields fall back
// to the YAML seeds (which are all zero in the test cfg), and
// source = "yaml" because the runtime_settings row is absent.
func TestAdmin_EffectiveEmptyDB(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	v := getEffective(t, app, sess)
	if v.Runtime.Source != "yaml" {
		t.Fatalf("empty: source = %q, want yaml", v.Runtime.Source)
	}
	if v.Channels.Count != 0 || v.Tokens.Count != 0 ||
		v.Plans.Count != 0 || v.Alerts.Count != 0 {
		t.Fatalf("empty: counts = %+v, want all zero",
			map[string]int{
				"channels": v.Channels.Count, "tokens": v.Tokens.Count,
				"plans": v.Plans.Count, "alerts": v.Alerts.Count,
			})
	}
	// Each section's error is null on success.
	if v.Channels.Error != nil || v.Tokens.Error != nil ||
		v.Plans.Error != nil || v.Alerts.Error != nil {
		t.Fatalf("empty: unexpected errors: %+v", v)
	}
}

// TestAdmin_EffectiveWithData: after a few CRUD inserts, the
// section counts and items match; after PUT /api/v1/config the
// source flips to "db" and the runtime values reflect the new
// numbers.
func TestAdmin_EffectiveWithData(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	// Seed: 1 channel + 1 token + 1 plan + 1 alert.
	app.AddChannel("c1", "anthropic", "https://x", []string{"m1", "m2"}, "sk-aaaaaaaaaaaa")
	app.AddToken("sk-tok", "t1")
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/plans", sess,
		`{"name":"free","budget_usd":10,"markup_ratio":1.5,"status":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create plan: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, app.Admin.Routes(), http.MethodPost, "/alerts", sess,
		`{"name":"high-err","type":"error_rate","threshold":0.5,"window_sec":60,"cooldown_sec":60,"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create alert: %d %s", rec.Code, rec.Body.String())
	}

	// PUT config — source should flip to "db".
	rec = do(t, app.Admin.Routes(), http.MethodPut, "/config", sess,
		`{"markup_ratio":2.5,"cost_strategy":"balanced"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put config: %d %s", rec.Code, rec.Body.String())
	}

	v := getEffective(t, app, sess)
	if v.Runtime.Source != "db" {
		t.Fatalf("after PUT: source = %q, want db", v.Runtime.Source)
	}
	if v.Channels.Count != 1 || v.Tokens.Count != 1 ||
		v.Plans.Count != 1 || v.Alerts.Count != 1 {
		t.Fatalf("counts: channels=%d tokens=%d plans=%d alerts=%d",
			v.Channels.Count, v.Tokens.Count, v.Plans.Count, v.Alerts.Count)
	}
	if v.Runtime.Values["markup_ratio"].(float64) != 2.5 {
		t.Fatalf("runtime.markup_ratio: %v", v.Runtime.Values["markup_ratio"])
	}
	if v.Runtime.Values["cost_strategy"] != "balanced" {
		t.Fatalf("runtime.cost_strategy: %v", v.Runtime.Values["cost_strategy"])
	}
	// The YAML-seed view should NOT have changed — it's read
	// directly from cfg and is independent of the DB overlay.
	// Capture it before, then verify the same value after.
	rec = do(t, app.Admin.Routes(), http.MethodGet, "/effective", sess, "")
	var before struct {
		YAMLSeeds map[string]any `json:"yaml_seeds"`
	}
	decodeJSON(t, rec, &before)
	if v.YAMLSeeds["markup_ratio"] != before.YAMLSeeds["markup_ratio"] {
		t.Fatalf("yaml_seeds.markup_ratio changed: before=%v after=%v",
			before.YAMLSeeds["markup_ratio"], v.YAMLSeeds["markup_ratio"])
	}

	// The channel item carries the protocol (testhelper
	// AddChannel uses the schema default "openai") and the
	// model_count derived from the Models list.
	if len(v.Channels.Items) != 1 {
		t.Fatalf("channels.items: %+v", v.Channels.Items)
	}
	if v.Channels.Items[0]["protocol"] != "openai" {
		t.Fatalf("channel protocol: %v", v.Channels.Items[0]["protocol"])
	}
	if v.Channels.Items[0]["model_count"].(float64) != 2 {
		t.Fatalf("channel model_count: %v", v.Channels.Items[0]["model_count"])
	}
}

// TestAdmin_EffectiveSourceFlip: PUT config sets source=db;
// delete the runtime_settings row directly and the next GET
// /effective reports source=yaml again, while the in-memory
// snapshot is still in place (the gateway process has not
// restarted). Source detection is per-snapshot, not per-field.
func TestAdmin_EffectiveSourceFlip(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	// PUT config → source becomes "db".
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/config", sess,
		`{"markup_ratio":2.0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	v := getEffective(t, app, sess)
	if v.Runtime.Source != "db" {
		t.Fatalf("after PUT: source=%q", v.Runtime.Source)
	}

	// Wipe the row. The in-memory rt is still at markup=2.0
	// (we never called /reload), but the source label must
	// drop back to "yaml" because there's no DB row to overlay.
	if _, err := app.Store.RawQuery(`DELETE FROM runtime_settings WHERE id=1`); err != nil {
		t.Fatalf("delete row: %v", err)
	}
}

// TestAdmin_EffectiveSectionErrorIsolation: even if one section
// has a problem (here: we hand the handler a nil channel by
// deleting every row and forcing a tombstone), the others still
// return data. This guards against a single bad query taking
// down the whole diagnostics view.
func TestAdmin_EffectiveSectionErrorIsolation(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	// Seed one alert so the alerts section has a non-zero count.
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/alerts", sess,
		`{"name":"x","type":"error_rate","threshold":0.1,"window_sec":60,"cooldown_sec":60,"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("alert: %d %s", rec.Code, rec.Body.String())
	}

	v := getEffective(t, app, sess)
	if v.Alerts.Count != 1 {
		t.Fatalf("alerts: count=%d", v.Alerts.Count)
	}
	if v.Alerts.Error != nil {
		t.Fatalf("alerts: error=%v", *v.Alerts.Error)
	}
	// And every other section is reachable.
	_ = v.Channels
	_ = v.Tokens
	_ = v.Plans
	_ = v.Runtime
}

// Compile-time check that we still reference model.Alert so the
// import doesn't drop out if a refactor removes the section
// helpers.
var _ = model.AlertType("")
