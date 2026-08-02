package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// TestAlertCreate_SavesRule: POST /alerts persists a new rule and
// redirects (previously 501 "alert save not yet wired").
func TestAlertCreate_SavesRule(t *testing.T) {
	h, st := newTestWebUI(t)
	_, cookie := newTestUser(t, h, h.store, withRole(model.RoleAdmin))

	rec := formPostWithCookie(t, h.Routes(), "/alerts", cookie, map[string]string{
		"name":         "high-error-rate",
		"type":         "error_rate",
		"threshold":    "0.5",
		"window_sec":   "60",
		"cooldown_sec": "300",
		"webhook_url":  "https://hooks.example.com/abc",
		"enabled":      "1",
	})
	assertRedirect(t, rec, "/admin/alerts")

	alerts, err := st.GetAlerts()
	if err != nil {
		t.Fatalf("GetAlerts: %v", err)
	}
	if len(alerts) != 1 || alerts[0].Name != "high-error-rate" || alerts[0].Type != model.AlertErrorRate {
		t.Fatalf("alerts = %+v", alerts)
	}
	if alerts[0].Threshold != 0.5 || alerts[0].WindowSec != 60 || alerts[0].CooldownSec != 300 {
		t.Fatalf("fields not decoded: %+v", alerts[0])
	}
	if !alerts[0].Enabled || alerts[0].WebhookURL != "https://hooks.example.com/abc" {
		t.Fatalf("enabled/webhook: %+v", alerts[0])
	}
}

// TestAlertCreate_Validation: missing name and unknown type are
// rejected before touching the store.
func TestAlertCreate_Validation(t *testing.T) {
	h, st := newTestWebUI(t)
	_, cookie := newTestUser(t, h, h.store, withRole(model.RoleAdmin))

	rec := formPostWithCookie(t, h.Routes(), "/alerts", cookie, map[string]string{
		"type": "error_rate",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing name: status = %d, want 400", rec.Code)
	}
	rec = formPostWithCookie(t, h.Routes(), "/alerts", cookie, map[string]string{
		"name": "x",
		"type": "bogus_type",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad type: status = %d, want 400", rec.Code)
	}
	alerts, _ := st.GetAlerts()
	if len(alerts) != 0 {
		t.Fatalf("store must be untouched, got %d alerts", len(alerts))
	}
}

// TestAlertUpdate_EditsRule: PUT (via _method) updates the rule.
func TestAlertUpdate_EditsRule(t *testing.T) {
	h, st := newTestWebUI(t)
	_, cookie := newTestUser(t, h, h.store, withRole(model.RoleAdmin))

	a := &model.Alert{Name: "old", Type: model.AlertErrorRate, Threshold: 0.1, Enabled: true}
	if err := st.CreateAlert(a); err != nil {
		t.Fatalf("seed alert: %v", err)
	}

	rec := formPostWithCookie(t, h.Routes(), "/alerts/"+itoa(a.ID), cookie, map[string]string{
		"_method":      "PUT",
		"name":         "renamed",
		"type":         "cost_spike",
		"threshold":    "2.5",
		"window_sec":   "120",
		"cooldown_sec": "600",
		"webhook_url":  "",
		"enabled":      "0",
	})
	assertRedirect(t, rec, "/admin/alerts")

	updated, err := st.GetAlert(a.ID)
	if err != nil {
		t.Fatalf("GetAlert: %v", err)
	}
	if updated.Name != "renamed" || updated.Type != model.AlertCostSpike || updated.Threshold != 2.5 {
		t.Fatalf("update not applied: %+v", updated)
	}
	if updated.Enabled {
		t.Fatalf("enabled should be false: %+v", updated)
	}
}

// TestAlertDelete_RemovesRule.
func TestAlertDelete_RemovesRule(t *testing.T) {
	h, st := newTestWebUI(t)
	_, cookie := newTestUser(t, h, h.store, withRole(model.RoleAdmin))

	a := &model.Alert{Name: "doomed", Type: model.AlertKeyExhausted, Threshold: 1, Enabled: true}
	if err := st.CreateAlert(a); err != nil {
		t.Fatalf("seed alert: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/alerts/"+itoa(a.ID), nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: status = %d, want 200", rec.Code)
	}
	alerts, _ := st.GetAlerts()
	if len(alerts) != 0 {
		t.Fatalf("alert still present: %+v", alerts)
	}
}

// TestAlertAck_RegistersRoute: the previously-404 ack button now
// acknowledges the event.
func TestAlertAck_RegistersRoute(t *testing.T) {
	h, st := newTestWebUI(t)
	_, cookie := newTestUser(t, h, h.store, withRole(model.RoleAdmin))

	a := &model.Alert{Name: "ack-me", Type: model.AlertErrorRate, Threshold: 0.5, Enabled: true}
	if err := st.CreateAlert(a); err != nil {
		t.Fatalf("seed alert: %v", err)
	}
	ev := &model.AlertEvent{AlertID: a.ID, AlertName: a.Name, AlertType: a.Type, FiredAt: nowAdd(-time.Minute)}
	if err := st.CreateAlertEvent(ev); err != nil {
		t.Fatalf("seed event: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/alerts/events/"+itoa(ev.ID)+"/ack", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ack: status = %d, want 200", rec.Code)
	}

	events, err := st.GetAlertEvents(10)
	if err != nil {
		t.Fatalf("GetAlertEvents: %v", err)
	}
	if len(events) != 1 || !events[0].Acknowledged {
		t.Fatalf("event not acknowledged: %+v", events)
	}
}

// TestAlertsPage_RendersFormLink: the page renders both rule and
// event tables (regression for the 501/404 era).
func TestAlertsPage_RendersFormLink(t *testing.T) {
	h, st := newTestWebUI(t)
	_, cookie := newTestUser(t, h, h.store, withRole(model.RoleAdmin))

	a := &model.Alert{Name: "rule-1", Type: model.AlertErrorRate, Threshold: 0.5, Enabled: true}
	if err := st.CreateAlert(a); err != nil {
		t.Fatalf("seed alert: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/alerts", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("page: status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "rule-1") {
		t.Errorf("rule name missing:\n%s", body)
	}
	if !strings.Contains(body, `action="/admin/alerts/`+itoa(a.ID)+`"`) && !strings.Contains(body, "/admin/alerts/"+itoa(a.ID)+"/edit") {
		t.Errorf("edit link missing:\n%s", body)
	}
}
