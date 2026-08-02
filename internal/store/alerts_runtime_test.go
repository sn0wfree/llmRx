package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

func TestAlerts_CRUD(t *testing.T) {
	s := openTemp(t)
	a := &model.Alert{
		Name: "high-errors", Type: model.AlertErrorRate,
		Threshold: 0.5, WindowSec: 300, CooldownSec: 60,
		WebhookURL: "https://example.com/hook", Enabled: true,
	}
	if err := s.CreateAlert(a); err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}
	if a.ID == 0 {
		t.Fatal("CreateAlert did not set ID")
	}
	got, err := s.GetAlert(a.ID)
	if err != nil {
		t.Fatalf("GetAlert: %v", err)
	}
	if got.Name != "high-errors" || got.Type != model.AlertErrorRate || !got.Enabled {
		t.Fatalf("GetAlert mismatch: %+v", got)
	}
	if got.Threshold != 0.5 || got.WindowSec != 300 || got.CooldownSec != 60 {
		t.Fatalf("alert fields mismatch: %+v", got)
	}
	got.Name = "low-errors"
	got.Threshold = 0.1
	got.Enabled = false
	if err := s.UpdateAlert(got); err != nil {
		t.Fatalf("UpdateAlert: %v", err)
	}
	updated, _ := s.GetAlert(a.ID)
	if updated.Name != "low-errors" || updated.Threshold != 0.1 || updated.Enabled {
		t.Fatalf("UpdateAlert not persisted: %+v", updated)
	}
	all, err := s.GetAlerts()
	if err != nil {
		t.Fatalf("GetAlerts: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("GetAlerts: expected 1, got %d", len(all))
	}
	if err := s.DeleteAlert(a.ID); err != nil {
		t.Fatalf("DeleteAlert: %v", err)
	}
	if _, err := s.GetAlert(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestAlerts_GetNotFound(t *testing.T) {
	s := openTemp(t)
	_, err := s.GetAlert(999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestAlerts_RecordFired(t *testing.T) {
	s := openTemp(t)
	a := &model.Alert{Name: "a", Type: model.AlertErrorRate, Threshold: 0.5, Enabled: true}
	if err := s.CreateAlert(a); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if changed, err := s.RecordAlertFired(a.ID, now); err != nil || !changed {
		t.Fatalf("RecordAlertFired: changed=%v err=%v", changed, err)
	}
	// Older timestamps lose the claim (multi-replica no double-fire).
	if changed, err := s.RecordAlertFired(a.ID, now-10); err != nil || changed {
		t.Fatalf("stale RecordAlertFired must lose: changed=%v err=%v", changed, err)
	}
	got, _ := s.GetAlert(a.ID)
	if got.LastFiredAt != now {
		t.Fatalf("LastFiredAt not set: got %d, want %d", got.LastFiredAt, now)
	}
}

func TestAlerts_DisableAlert(t *testing.T) {
	s := openTemp(t)
	a := &model.Alert{Name: "a", Type: model.AlertCostSpike, Threshold: 3.0, Enabled: true}
	if err := s.CreateAlert(a); err != nil {
		t.Fatal(err)
	}
	if err := s.DisableAlert(a.ID, "window too long"); err != nil {
		t.Fatalf("DisableAlert: %v", err)
	}
	got, _ := s.GetAlert(a.ID)
	if got.Enabled {
		t.Fatal("alert should be disabled")
	}
	if got.DisabledReason != "window too long" {
		t.Fatalf("reason not persisted: %q", got.DisabledReason)
	}
}

func TestAlerts_DisableNonexistent(t *testing.T) {
	s := openTemp(t)
	if err := s.DisableAlert(999, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestAlertEvents_CRUD(t *testing.T) {
	s := openTemp(t)
	a := &model.Alert{Name: "evt-alert", Type: model.AlertP95Latency, Threshold: 5000, Enabled: true}
	if err := s.CreateAlert(a); err != nil {
		t.Fatal(err)
	}
	evt := &model.AlertEvent{
		AlertID:          a.ID,
		AlertName:        a.Name,
		AlertType:        a.Type,
		FiredAt:          time.Now(),
		Payload:          `{"p95": 6000}`,
		DeliveredWebhook: true,
	}
	if err := s.CreateAlertEvent(evt); err != nil {
		t.Fatalf("CreateAlertEvent: %v", err)
	}
	if evt.ID == 0 {
		t.Fatal("CreateAlertEvent did not set ID")
	}
	events, err := s.GetAlertEvents(10)
	if err != nil {
		t.Fatalf("GetAlertEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].AlertName != "evt-alert" || !events[0].DeliveredWebhook {
		t.Fatalf("event mismatch: %+v", events[0])
	}
	if events[0].Acknowledged {
		t.Fatal("new event should not be acknowledged")
	}
	if err := s.AckAlertEvent(evt.ID); err != nil {
		t.Fatalf("AckAlertEvent: %v", err)
	}
	events, _ = s.GetAlertEvents(10)
	if !events[0].Acknowledged {
		t.Fatal("event should be acknowledged after AckAlertEvent")
	}
}

func TestAlertEvents_DefaultLimit(t *testing.T) {
	s := openTemp(t)
	a := &model.Alert{Name: "a", Type: model.AlertErrorRate, Threshold: 0.5, Enabled: true}
	if err := s.CreateAlert(a); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := s.CreateAlertEvent(&model.AlertEvent{
			AlertID: a.ID, AlertName: a.Name, AlertType: a.Type,
			FiredAt: time.Now(), Payload: "{}",
		}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := s.GetAlertEvents(0)
	if err != nil {
		t.Fatalf("GetAlertEvents(0): %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("limit=0 should default to 100, got %d events", len(events))
	}
}

func TestAlertEvents_OrderedByFiredAtDesc(t *testing.T) {
	s := openTemp(t)
	a := &model.Alert{Name: "a", Type: model.AlertErrorRate, Threshold: 0.5, Enabled: true}
	if err := s.CreateAlert(a); err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	for i := 0; i < 3; i++ {
		if err := s.CreateAlertEvent(&model.AlertEvent{
			AlertID: a.ID, AlertName: a.Name, AlertType: a.Type,
			FiredAt: base.Add(time.Duration(i) * time.Second), Payload: "{}",
		}); err != nil {
			t.Fatal(err)
		}
	}
	events, _ := s.GetAlertEvents(10)
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if !events[0].FiredAt.After(events[2].FiredAt) {
		t.Fatal("events should be ordered by fired_at DESC")
	}
}

func TestRuntimeSettings_EmptyByDefault(t *testing.T) {
	s := openTemp(t)
	raw, err := s.GetRuntimeSettings()
	if err != nil {
		t.Fatalf("GetRuntimeSettings: %v", err)
	}
	if raw != nil {
		t.Fatalf("expected nil when no settings, got %v", raw)
	}
}

func TestRuntimeSettings_Upsert(t *testing.T) {
	s := openTemp(t)
	payload1 := []byte(`{"cost_strategy":"balanced","markup_ratio":1.2}`)
	if err := s.SetRuntimeSettings(payload1); err != nil {
		t.Fatalf("SetRuntimeSettings first: %v", err)
	}
	raw, err := s.GetRuntimeSettings()
	if err != nil {
		t.Fatalf("GetRuntimeSettings: %v", err)
	}
	if string(raw) != string(payload1) {
		t.Fatalf("round-trip mismatch: got %s", raw)
	}
	payload2 := []byte(`{"cost_strategy":"cheapest","markup_ratio":1.0}`)
	if err := s.SetRuntimeSettings(payload2); err != nil {
		t.Fatalf("SetRuntimeSettings upsert: %v", err)
	}
	raw, _ = s.GetRuntimeSettings()
	if string(raw) != string(payload2) {
		t.Fatalf("upsert mismatch: got %s", raw)
	}
}

func TestRuntimeSettings_RoundTripJSON(t *testing.T) {
	s := openTemp(t)
	original := map[string]any{
		"cost_strategy":      "balanced",
		"markup_ratio":       1.3,
		"log_retention_days": 30,
		"stream_timeout_sec": 300,
	}
	payload, _ := json.Marshal(original)
	if err := s.SetRuntimeSettings(payload); err != nil {
		t.Fatal(err)
	}
	raw, _ := s.GetRuntimeSettings()
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if decoded["cost_strategy"] != "balanced" {
		t.Fatalf("cost_strategy mismatch: %v", decoded["cost_strategy"])
	}
}

func TestRawQuery_Rows(t *testing.T) {
	s := openTemp(t)
	s.CreateChannel(&model.Channel{Name: "rc", Provider: "x", BaseURL: "x", Status: model.ChannelEnabled})
	rows, err := s.RawQuery(`SELECT id, name FROM channels WHERE name = ?`, "rc")
	if err != nil {
		t.Fatalf("RawQuery: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name != "rc" {
			t.Fatalf("name mismatch: %s", name)
		}
		count++
	}
	if count != 1 {
		t.Fatalf("expected 1 row, got %d", count)
	}
}

func TestRawQueryRow_NoMatch(t *testing.T) {
	s := openTemp(t)
	var name string
	err := s.RawQueryRow(`SELECT name FROM channels WHERE id = ?`, 999).Scan(&name)
	if err == nil {
		t.Fatal("expected error for no match")
	}
}

func TestPing(t *testing.T) {
	s := openTemp(t)
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}
