package webui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/sn0wfree/llmRx/internal/model"
)

// chiCtx returns a request context populated with chi RouteContext so
// chi.URLParam can look up "id". Used by direct handler-call tests
// that bypass the chi router.
func chiCtx(req *http.Request, params map[string]string) *http.Request {
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestSetStore_ReplacesStore(t *testing.T) {
	h, _ := newTestWebUI(t)
	mock := NewScriptedStore(nil)
	h.SetStore(mock)
	if h.store != WebuiStore(mock) {
		t.Fatalf("SetStore did not replace store")
	}
}

func TestSetStore_NilStore(t *testing.T) {
	h, _ := newTestWebUI(t)
	h.SetStore(nil)
	if h.store != nil {
		t.Fatalf("SetStore(nil) did not clear store")
	}
}

func TestChannelUpdate_DirectCallBadID(t *testing.T) {
	h, _ := newTestWebUI(t)
	req := httptest.NewRequest(http.MethodPut, "/channels/abc", nil)
	req = chiCtx(req, map[string]string{"id": "abc"})
	rec := httptest.NewRecorder()
	h.ChannelUpdate(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestChannelUpdate_DirectCallNotFound(t *testing.T) {
	h, _ := newTestWebUI(t)
	req := httptest.NewRequest(http.MethodPut, "/channels/9999", nil)
	req = chiCtx(req, map[string]string{"id": "9999"})
	rec := httptest.NewRecorder()
	h.ChannelUpdate(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestChannelUpdate_DirectCallSuccess(t *testing.T) {
	h, st := newTestWebUI(t)
	ch := newCh(t, st, "ch1", "openai")
	req := httptest.NewRequest(http.MethodPut, "/channels/"+itoa(ch.ID), nil)
	req = chiCtx(req, map[string]string{"id": itoa(ch.ID)})
	rec := httptest.NewRecorder()
	h.ChannelUpdate(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d want 303, body=%s", rec.Code, rec.Body.String())
	}
}

func TestTokenUpdate_DirectCallBadID(t *testing.T) {
	h, _ := newTestWebUI(t)
	req := httptest.NewRequest(http.MethodPut, "/tokens/xyz", nil)
	req = chiCtx(req, map[string]string{"id": "xyz"})
	rec := httptest.NewRecorder()
	h.TokenUpdate(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestTokenUpdate_DirectCallNotFound(t *testing.T) {
	h, _ := newTestWebUI(t)
	req := httptest.NewRequest(http.MethodPut, "/tokens/99999", nil)
	req = chiCtx(req, map[string]string{"id": "99999"})
	rec := httptest.NewRecorder()
	h.TokenUpdate(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestTokenUpdate_DirectCallSuccess(t *testing.T) {
	h, st := newTestWebUI(t)
	tk := &model.Token{
		Name: "tok1", Key: "sk-test-token-1234567890abcdef",
		Status: model.TokenActive, RPM: 60, TPM: 1000,
	}
	if err := st.CreateToken(tk); err != nil {
		t.Fatalf("create token: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/tokens/"+itoa(tk.ID), nil)
	req = chiCtx(req, map[string]string{"id": itoa(tk.ID)})
	rec := httptest.NewRecorder()
	h.TokenUpdate(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d want 303, body=%s", rec.Code, rec.Body.String())
	}
}

func TestChannelDelete_DirectCallBadID(t *testing.T) {
	h, _ := newTestWebUI(t)
	req := httptest.NewRequest(http.MethodDelete, "/channels/abc", nil)
	req = chiCtx(req, map[string]string{"id": "abc"})
	rec := httptest.NewRecorder()
	h.ChannelDelete(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestChannelDelete_DirectCallStoreError(t *testing.T) {
	h, st := newTestWebUI(t)
	mock := NewScriptedStore(st)
	mock.DeleteChannelFunc = func(id int64) error { return errors.New("boom") }
	h.SetStore(mock)
	req := httptest.NewRequest(http.MethodDelete, "/channels/1", nil)
	req = chiCtx(req, map[string]string{"id": "1"})
	rec := httptest.NewRecorder()
	h.ChannelDelete(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestChannelDelete_DirectCallSuccess(t *testing.T) {
	h, st := newTestWebUI(t)
	ch := newCh(t, st, "ch-del", "openai")
	mock := NewScriptedStore(st)
	h.SetStore(mock)
	req := httptest.NewRequest(http.MethodDelete, "/channels/"+itoa(ch.ID), nil)
	req = chiCtx(req, map[string]string{"id": itoa(ch.ID)})
	rec := httptest.NewRecorder()
	h.ChannelDelete(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", rec.Code)
	}
}
