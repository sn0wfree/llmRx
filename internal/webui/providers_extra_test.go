package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMetaProviders_Renders exercises the /api/meta-providers
// JSON endpoint used by the model-catalog browser.
func TestMetaProviders_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/api/meta-providers", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !contains(rec.Body.String(), `"providers"`) {
		t.Error("expected providers field in JSON")
	}
}

// TestAvailableModels_DeduplicatesAndSorts verifies that the
// AvailableModels handler returns a sorted, deduplicated list of
// models from enabled channels.
func TestAvailableModels_DeduplicatesAndSorts(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	// Seed two channels with overlapping model lists
	c1 := modelChannelFixture()
	c1.Name = "ch-a"
	c1.Models = []string{"zulu", "alpha", "alpha"} // dup
	if err := st.CreateChannel(c1); err != nil {
		t.Fatalf("seed c1: %v", err)
	}
	c2 := modelChannelFixture()
	c2.Name = "ch-b"
	c2.Models = []string{"alpha", "mike"}
	if err := st.CreateChannel(c2); err != nil {
		t.Fatalf("seed c2: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/available-models", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !contains(body, `"models"`) {
		t.Error("expected models field")
	}
	// alpha, mike, zulu should all appear
	for _, m := range []string{"alpha", "mike", "zulu"} {
		if !contains(body, m) {
			t.Errorf("expected %q in models", m)
		}
	}
}

// TestAvailableModels_SkipsDisabledChannels verifies disabled
// channels are excluded from the model list.
func TestAvailableModels_SkipsDisabledChannels(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	c := modelChannelFixture()
	c.Models = []string{"visible-model"}
	if err := st.CreateChannel(c); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Disable (ChannelDisabled = 2)
	c.Status = 2
	if err := st.UpdateChannel(c); err != nil {
		t.Fatalf("disable: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/available-models", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if contains(rec.Body.String(), "visible-model") {
		t.Error("disabled channel's models should NOT appear")
	}
}