package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

func newCh(t *testing.T, st interface {
	CreateChannel(*model.Channel) error
}, name, provider string) *model.Channel {
	t.Helper()
	ch := &model.Channel{
		Name: name, Provider: provider, Protocol: "openai",
		BaseURL: "https://x", Models: []string{"gpt-4"},
		Status: model.ChannelEnabled,
	}
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	return ch
}

func TestChannelsPage_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	newCh(t, st, "ch1", "openai")

	req := httptest.NewRequest(http.MethodGet, "/channels", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ch1") {
		t.Errorf("body should contain channel name")
	}
}

func TestChannelNewForm_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/channels/new", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestChannelCreate_Success(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	body := "name=ch1&provider=openai&base_url=https://x&models=gpt-4&priority=5&input_price=1&output_price=2&status=1"
	req := httptest.NewRequest(http.MethodPost, "/channels", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	chs, _ := st.GetChannels()
	if len(chs) != 1 || chs[0].Name != "ch1" {
		t.Errorf("expected 1 channel ch1, got %+v", chs)
	}
}

func TestChannelCreate_ValidationError(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	body := "name=&provider=&base_url=&models="
	req := httptest.NewRequest(http.MethodPost, "/channels", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	chs, _ := st.GetChannels()
	if len(chs) != 0 {
		t.Errorf("no channel should be created")
	}
}

func TestChannelEditForm_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	ch := newCh(t, st, "ch1", "openai")

	req := httptest.NewRequest(http.MethodGet, "/channels/"+itoa(ch.ID)+"/edit", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestChannelEditForm_BadID(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)

	req := httptest.NewRequest(http.MethodGet, "/channels/abc/edit", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestChannelAction_Update(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	ch := newCh(t, st, "ch1", "openai")

	body := "_method=PUT&name=ch2&provider=anthropic&base_url=https://y&models=claude-3&status=1"
	req := httptest.NewRequest(http.MethodPost, "/channels/"+itoa(ch.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	updated, _ := st.GetChannel(ch.ID)
	if updated.Name != "ch2" {
		t.Errorf("name=%q want ch2", updated.Name)
	}
}

func TestChannelAction_Delete(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	ch := newCh(t, st, "ch1", "openai")

	body := "_method=DELETE"
	req := httptest.NewRequest(http.MethodPost, "/channels/"+itoa(ch.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	chs, _ := st.GetChannels()
	if len(chs) != 0 {
		t.Errorf("channel should be deleted")
	}
}

func TestChannelAction_BadMethod(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	ch := newCh(t, st, "ch1", "openai")

	body := "_method=PATCH"
	req := httptest.NewRequest(http.MethodPost, "/channels/"+itoa(ch.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code=%d want 405", rec.Code)
	}
}

func TestChannelDelete_ViaDELETE(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	ch := newCh(t, st, "ch1", "openai")

	req := httptest.NewRequest(http.MethodDelete, "/channels/"+itoa(ch.ID), nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestChannelsListPartial_Search(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	newCh(t, st, "alpha", "openai")
	newCh(t, st, "beta", "anthropic")

	req := httptest.NewRequest(http.MethodGet, "/channels/partial/list?q=alpha", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "alpha") {
		t.Errorf("body should contain alpha")
	}
	if strings.Contains(rec.Body.String(), "beta") {
		t.Errorf("body should not contain beta")
	}
}

func TestChannelKeysPage_Renders(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	ch := newCh(t, st, "ch1", "openai")
	st.CreateKey(&model.Key{ChannelID: ch.ID, Key: "sk-test", KeyMasked: "sk-t***test", Status: model.KeyActive})

	req := httptest.NewRequest(http.MethodGet, "/channels/"+itoa(ch.ID)+"/keys", nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestChannelKeyCreate_Success(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	ch := newCh(t, st, "ch1", "openai")

	body := "key=sk-new-key-12345"
	req := httptest.NewRequest(http.MethodPost, "/channels/"+itoa(ch.ID)+"/keys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	keys, _ := st.GetKeys(ch.ID)
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
}

func TestChannelKeyCreate_EmptyKey(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	ch := newCh(t, st, "ch1", "openai")

	body := "key="
	req := httptest.NewRequest(http.MethodPost, "/channels/"+itoa(ch.ID)+"/keys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestChannelKeyDelete_Success(t *testing.T) {
	h, st := newTestWebUI(t)
	admin, _ := st.GetUserByUsername("admin")
	tok := sessionCookieFor(t, st, admin)
	ch := newCh(t, st, "ch1", "openai")
	k := &model.Key{ChannelID: ch.ID, Key: "sk-test", KeyMasked: "sk-t***test", Status: model.KeyActive}
	st.CreateKey(k)

	req := httptest.NewRequest(http.MethodDelete, "/channels/"+itoa(ch.ID)+"/keys/"+itoa(k.ID), nil)
	req.AddCookie(&http.Cookie{Name: "llmrx_session", Value: tok})
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	keys, _ := st.GetKeys(ch.ID)
	if len(keys) != 0 {
		t.Errorf("key should be deleted")
	}
}
