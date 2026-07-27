package alert

import (
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// evalP95 branches:
//   - total < 5  → false (too few samples)
//   - n < 1      → n=1
//   - avgMS >= threshold → fire with payload
//   - avgMS <  threshold → no fire

func TestEvaluateP95_TooFewSamples(t *testing.T) {
	st := newStore(t)
	now := time.Now()
	// 4 samples < 5 → must not fire regardless of latency.
	for i := 0; i < 4; i++ {
		_ = st.CreateLog(&model.Log{
			Model: "m", DurationMs: 100000, CreatedAt: now.Add(-time.Duration(i) * time.Second),
		})
	}
	r := &model.Alert{Type: model.AlertP95Latency, WindowSec: 60, Threshold: 1}
	fired, payload, err := Evaluate(r, now, st)
	if err != nil {
		t.Fatal(err)
	}
	if fired {
		t.Errorf("expected no fire with 4 samples, got fired=%v payload=%v", fired, payload)
	}
}

func TestEvaluateP95_BelowThreshold(t *testing.T) {
	st := newStore(t)
	now := time.Now()
	// 20 fast samples (10ms each). p95 ≈ 10ms < 1000ms threshold.
	for i := 0; i < 20; i++ {
		_ = st.CreateLog(&model.Log{
			Model: "m", DurationMs: 10, CreatedAt: now.Add(-time.Duration(i+1) * time.Second),
		})
	}
	r := &model.Alert{Type: model.AlertP95Latency, WindowSec: 60, Threshold: 1000}
	fired, _, err := Evaluate(r, now, st)
	if err != nil {
		t.Fatal(err)
	}
	if fired {
		t.Error("expected no fire: all samples well below threshold")
	}
}

func TestEvaluateP95_PayloadFields(t *testing.T) {
	st := newStore(t)
	now := time.Now()
	for i := 0; i < 10; i++ {
		_ = st.CreateLog(&model.Log{
			Model: "m", DurationMs: int64(1000 + i*100),
			CreatedAt: now.Add(-time.Duration(i+1) * time.Second),
		})
	}
	r := &model.Alert{Type: model.AlertP95Latency, WindowSec: 60, Threshold: 500}
	fired, payload, err := Evaluate(r, now, st)
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("expected fire: worst 5% > 500ms")
	}
	// Payload must include the four documented fields.
	for _, k := range []string{"window_sec", "requests", "p95_ms", "threshold_ms"} {
		if _, ok := payload[k]; !ok {
			t.Errorf("payload missing field %q", k)
		}
	}
	if got := payload["requests"].(int64); got != 10 {
		t.Errorf("requests = %d, want 10", got)
	}
	if got := payload["window_sec"].(int64); got != 60 {
		t.Errorf("window_sec = %d, want 60", got)
	}
	if got := payload["threshold_ms"].(float64); got != 500 {
		t.Errorf("threshold_ms = %v, want 500", got)
	}
}

// evalKeyExhausted branches: an enabled channel with no active keys → fire.
func TestEvaluateKeyExhausted_NoActiveKeys(t *testing.T) {
	st := newStore(t)
	// Create an enabled channel with no keys → must fire.
	_ = st.CreateChannel(&model.Channel{
		Name: "exhausted", Provider: "openai", Protocol: "openai",
		BaseURL: "https://x", Models: []string{"m"},
		Status: model.ChannelEnabled,
	})
	r := &model.Alert{Type: model.AlertKeyExhausted, WindowSec: 60, Threshold: 1}
	fired, payload, err := Evaluate(r, time.Now(), st)
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Error("enabled channel with no keys should fire")
	}
	if _, ok := payload["drained_channels"]; !ok {
		t.Error("payload missing drained_channels")
	}
}

func TestEvaluateKeyExhausted_AllChannelsHaveKeys(t *testing.T) {
	st := newStore(t)
	ch := &model.Channel{
		Name: "ok", Provider: "openai", Protocol: "openai",
		BaseURL: "https://x", Models: []string{"m"},
		Status: model.ChannelEnabled,
	}
	if err := st.CreateChannel(ch); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateKey(&model.Key{
		ChannelID: ch.ID, Key: "sk-aaaaaaaaaaaaaaaaaaaa",
		KeyMasked: "sk-a***aaaa", Status: model.KeyActive,
	}); err != nil {
		t.Fatal(err)
	}
	r := &model.Alert{Type: model.AlertKeyExhausted, WindowSec: 60, Threshold: 1}
	fired, _, err := Evaluate(r, time.Now(), st)
	if err != nil {
		t.Fatal(err)
	}
	if fired {
		t.Error("channel with active key should not fire")
	}
}
