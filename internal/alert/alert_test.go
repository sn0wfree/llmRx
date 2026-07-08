package alert

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/runtime"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

type recordingChannel struct {
	name  string
	count int32
}

func (r *recordingChannel) Name() string { return r.name }
func (r *recordingChannel) Deliver(ev *model.AlertEvent) error {
	atomic.AddInt32(&r.count, 1)
	return nil
}

func newStore(t *testing.T) store.Store {
	app := testhelper.New(t)
	return app.Store
}

func TestEvaluateErrorRate(t *testing.T) {
	st := newStore(t)
	now := time.Now()
	// Seed 10 logs: 6 errors, 4 success.
	for i := 0; i < 10; i++ {
		st := st
		code := 200
		if i < 6 {
			code = 500
		}
		_ = st.CreateLog(&model.Log{Model: "m", StatusCode: code, CreatedAt: now.Add(-time.Duration(i) * time.Second)})
	}
	r := &model.Alert{Type: model.AlertErrorRate, WindowSec: 60, Threshold: 0.5}
	fired, payload, err := Evaluate(r, now, st)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if !fired {
		t.Fatal("expected fire (6/10=0.6 >= 0.5)")
	}
	if payload["error_ratio"].(float64) < 0.5 {
		t.Fatalf("payload ratio: %v", payload)
	}
}

func TestEvaluateErrorRateBelowNoise(t *testing.T) {
	st := newStore(t)
	now := time.Now()
	// 4 errors, 0 success: 100% but only 4 samples < 5.
	for i := 0; i < 4; i++ {
		_ = st.CreateLog(&model.Log{Model: "m", StatusCode: 500, CreatedAt: now.Add(-time.Duration(i) * time.Second)})
	}
	r := &model.Alert{Type: model.AlertErrorRate, WindowSec: 60, Threshold: 0.1}
	fired, _, err := Evaluate(r, now, st)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if fired {
		t.Fatal("expected no fire below noise threshold (4 < 5 samples)")
	}
}

func TestEvaluateKeyExhausted(t *testing.T) {
	st := newStore(t)
	// Channel with no keys.
	if err := st.CreateChannel(&model.Channel{
		Name: "exhausted", Provider: "p", BaseURL: "https://x", Status: model.ChannelEnabled,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	r := &model.Alert{Type: model.AlertKeyExhausted, WindowSec: 60}
	fired, payload, err := Evaluate(r, time.Now(), st)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if !fired {
		t.Fatal("expected fire: channel has no active keys")
	}
	dc, _ := payload["drained_channels"].([]string)
	if len(dc) == 0 {
		t.Fatalf("expected drained_channels non-empty, got: %v", payload)
	}
}

func TestManagerFireWebhook(t *testing.T) {
	st := newStore(t)
	// Capture webhook deliveries.
	var hits int32
	var lastBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		lastBody = buf
		w.WriteHeader(200)
	}))
	defer srv.Close()

	// Seed an error so error_rate fires.
	now := time.Now()
	for i := 0; i < 10; i++ {
		_ = st.CreateLog(&model.Log{Model: "m", StatusCode: 500, CreatedAt: now.Add(-time.Duration(i) * time.Second)})
	}

	a := &model.Alert{
		Name: "test", Type: model.AlertErrorRate, Threshold: 0.5,
		WindowSec: 60, CooldownSec: 0, WebhookURL: srv.URL, Enabled: true,
	}
	if err := st.CreateAlert(a); err != nil {
		t.Fatalf("create alert: %v", err)
	}

	wh := channelsShim{srv: srv, hits: &hits, lastBody: &lastBody}
	_ = wh
	m := NewManager(st, []Channel{
		&recordingChannel{name: "builtin"},
		&webhookShim{url: srv.URL, hits: &hits, lastBody: &lastBody},
	}, Config{TickInterval: time.Hour, DefaultCooldown: time.Millisecond})
	if err := m.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	m.evaluate(context.Background())
	if atomic.LoadInt32(&hits) == 0 {
		t.Fatal("webhook not hit")
	}
	evs, _ := st.GetAlertEvents(10)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	if !evs[0].DeliveredWebhook {
		t.Fatal("event not marked delivered")
	}
}

type webhookShim struct {
	url      string
	hits     *int32
	lastBody *[]byte
}

func (w *webhookShim) Name() string { return "webhook" }
func (w *webhookShim) Deliver(ev *model.AlertEvent) error {
	atomic.AddInt32(w.hits, 1)
	*w.lastBody = []byte(ev.Payload)
	return nil
}

type channelsShim struct {
	srv      *httptest.Server
	hits     *int32
	lastBody *[]byte
}

func TestManagerCooldownSuppresses(t *testing.T) {
	st := newStore(t)
	var hits int32
	a := &model.Alert{
		Name: "always", Type: model.AlertErrorRate, Threshold: 0,
		WindowSec: 60, CooldownSec: 600, Enabled: true,
	}
	_ = st.CreateAlert(a)
	// Insert logs after the alert so the row exists with no firing.
	now := time.Now()
	for i := 0; i < 10; i++ {
		_ = st.CreateLog(&model.Log{Model: "m", StatusCode: 500, CreatedAt: now.Add(-time.Duration(i) * time.Second)})
	}
	// LastFiredAt == 0 + WindowSec 60 has at least 10 samples, but
	// the test here is for the cooldown gate. Pre-fire via store,
	// then re-evaluate and ensure no second fire.
	if err := st.RecordAlertFired(a.ID, now.Unix()); err != nil {
		t.Fatalf("record: %v", err)
	}
	m := NewManager(st, []Channel{&recordingChannel{name: "builtin"}}, Config{DefaultCooldown: time.Hour})
	if err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	m.evaluate(context.Background())
	evs, _ := st.GetAlertEvents(10)
	if len(evs) != 0 {
		t.Fatalf("expected 0 events (cooldown), got %d", len(evs))
	}
	_ = hits
}

func TestEvaluateUnknownType(t *testing.T) {
	st := newStore(t)
	r := &model.Alert{Type: model.AlertType("bogus"), WindowSec: 60}
	_, _, err := Evaluate(r, time.Now(), st)
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestEvaluateCostSpike(t *testing.T) {
	st := newStore(t)
	now := time.Now()
	w := 60
	// Previous window: $1.00
	if err := st.CreateLog(&model.Log{Model: "m", RealCostUSD: 1.0, CreatedAt: now.Add(-time.Duration(w+30) * time.Second)}); err != nil {
		t.Fatal(err)
	}
	// Current window: $5.00 -> 5x spike
	if err := st.CreateLog(&model.Log{Model: "m", RealCostUSD: 5.0, CreatedAt: now.Add(-time.Duration(5) * time.Second)}); err != nil {
		t.Fatal(err)
	}
	r := &model.Alert{Type: model.AlertCostSpike, WindowSec: int64(w), Threshold: 2.0}
	fired, payload, err := Evaluate(r, now, st)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if !fired {
		t.Fatal("expected fire (5x >= 2x)")
	}
	if payload["spike_ratio"].(float64) < 2.0 {
		t.Fatalf("ratio: %v", payload)
	}
}

func TestEvaluateP95(t *testing.T) {
	st := newStore(t)
	now := time.Now()
	// 20 logs: 19 fast, 1 very slow
	for i := 0; i < 19; i++ {
		_ = st.CreateLog(&model.Log{Model: "m", DurationMs: int64(100 + i), CreatedAt: now.Add(-time.Duration(i+1) * time.Second)})
	}
	_ = st.CreateLog(&model.Log{Model: "m", DurationMs: 10000, CreatedAt: now.Add(-time.Duration(20) * time.Second)})
	r := &model.Alert{Type: model.AlertP95Latency, WindowSec: 60, Threshold: 5000}
	fired, _, err := Evaluate(r, now, st)
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("expected fire (worst 5% avg = 10000ms >= 5000)")
	}
}

// TestManagerCooldownFollowsRuntimeDefaults covers B2: when
// Config.Defaults is wired, the manager reads AlertCooldownSec() on
// every evaluate() call. Mutating rt between calls changes the
// effective cooldown without restarting the manager.
func TestManagerCooldownFollowsRuntimeDefaults(t *testing.T) {
	st := newStore(t)
	rt := runtime.New()
	rt.SetAlertCooldownSec(60) // initial: 1 minute

	a := &model.Alert{
		Name: "default-cooldown", Type: model.AlertErrorRate, Threshold: 0,
		WindowSec: 60, CooldownSec: 0, // 0 = use default
		Enabled:   true,
	}
	if err := st.CreateAlert(a); err != nil {
		t.Fatalf("create alert: %v", err)
	}

	now := time.Now()
	for i := 0; i < 10; i++ {
		_ = st.CreateLog(&model.Log{Model: "m", StatusCode: 500, CreatedAt: now.Add(-time.Duration(i) * time.Second)})
	}

	m := NewManager(st, []Channel{&recordingChannel{name: "builtin"}}, Config{
		Defaults: rt,
	})
	if err := m.Reload(); err != nil {
		t.Fatal(err)
	}

	// First eval: fires and records LastFiredAt = now.
	m.evaluate(context.Background())
	evs, _ := st.GetAlertEvents(10)
	if len(evs) != 1 {
		t.Fatalf("first eval: expected 1 event, got %d", len(evs))
	}

	// Immediate re-eval: cooldown=60s gates the second fire.
	m.evaluate(context.Background())
	evs, _ = st.GetAlertEvents(10)
	if len(evs) != 1 {
		t.Fatalf("within 60s cooldown: expected still 1, got %d", len(evs))
	}

	// Bypass the cooldown by zeroing it via the runtime. The next
	// eval must observe the new value (proving B2 fix).
	rt.SetAlertCooldownSec(0)
	m.evaluate(context.Background())
	evs, _ = st.GetAlertEvents(10)
	if len(evs) != 2 {
		t.Fatalf("after rt.SetAlertCooldownSec(0): expected 2 events, got %d", len(evs))
	}
}
