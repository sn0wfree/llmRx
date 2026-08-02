package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDashboardPage_QuickStartWhenEmpty verifies the quick-start
// card renders when the deployment has zero channels OR zero
// tokens. Phase A6 UX improvement.
func TestDashboardPage_QuickStartWhenEmpty(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if !contains(rec.Body.String(), "5 分钟快速上手") {
		t.Error("expected quick-start card when deployment empty")
	}
}

// TestDashboardPage_QuickStartHiddenWhenWired verifies the
// quick-start card is NOT shown once channels and tokens exist.
func TestDashboardPage_QuickStartHiddenWhenWired(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	// Seed one channel and one token
	ch := modelChannelFixture()
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if err := newComboToken(t, st, "wired"); err == nil {
	}

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	// Once wired, the quick-start card should NOT appear.
	if contains(rec.Body.String(), "5 分钟快速上手") {
		t.Error("quick-start card should be hidden when channels+tokens exist")
	}
}

// TestDashboardPage_QuickStartPartialOnlyChannels verifies the
// card shows when only channels exist (tokens missing) so the
// operator sees step 2 marked incomplete.
func TestDashboardPage_QuickStartPartialOnlyChannels(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	ch := modelChannelFixture()
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if !contains(rec.Body.String(), "5 分钟快速上手") {
		t.Error("quick-start should appear when only channels exist")
	}
	if !contains(rec.Body.String(), "✅ 通道已配置") {
		t.Error("step 1 should be marked done")
	}
}
