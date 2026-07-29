package webui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/modelmeta"
	"github.com/sn0wfree/llmRx/internal/provider"
)

func TestProvidersPage(t *testing.T) {
	h, _ := newScriptedWebui(t)
	sess := testSession(t, h)

	rec := httptest.NewRecorder()
	req := authReq2(t, http.MethodGet, "/admin/providers", sess)
	h.ProvidersPage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "供应商管理") {
		t.Error("page should contain 供应商管理 title")
	}
}

func TestProviderCreate_Success(t *testing.T) {
	h, _ := newScriptedWebui(t)
	sess := testSession(t, h)

	form := url.Values{}
	form.Set("name", "testprov")
	form.Set("display_name", "Test Provider")
	form.Set("protocol", "openai")
	form.Set("base_url", "https://test.example.com/v1")

	rec := httptest.NewRecorder()
	req := authReq2(t, http.MethodPost, "/admin/providers/create", sess)
	req.Body = io.NopCloser(strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	h.ProviderCreate(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d body=%s", rec.Code, rec.Body.String())
	}
	defs, err := h.store.GetProviderDefs()
	if err != nil {
		t.Fatalf("GetProviderDefs: %v", err)
	}
	found := false
	for _, d := range defs {
		if d.Name == "testprov" {
			found = true
			if d.Protocol != "openai" {
				t.Errorf("protocol: got %q", d.Protocol)
			}
			break
		}
	}
	if !found {
		t.Error("provider should be created")
	}
}

func TestProviderCreate_MissingFields(t *testing.T) {
	h, _ := newScriptedWebui(t)
	sess := testSession(t, h)

	form := url.Values{}
	form.Set("name", "incomplete")

	rec := httptest.NewRecorder()
	req := authReq2(t, http.MethodPost, "/admin/providers/create", sess)
	req.Body = io.NopCloser(strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	h.ProviderCreate(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestProviderCreate_BadForm(t *testing.T) {
	h, _ := newScriptedWebui(t)
	sess := testSession(t, h)

	rec := httptest.NewRecorder()
	req := authReq2(t, http.MethodPost, "/admin/providers/create", sess)
	req.Body = io.NopCloser(strings.NewReader("bad%ZZform"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	h.ProviderCreate(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestProviderDelete_Success(t *testing.T) {
	h, _ := newScriptedWebui(t)
	sess := testSession(t, h)

	pd := &model.ProviderDef{Name: "delprov", Protocol: "openai", BaseURL: "https://x"}
	if err := h.store.CreateProviderDef(pd); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	rec := httptest.NewRecorder()
	req := authReq2(t, http.MethodDelete, "/admin/providers/"+itoaStr(pd.ID), sess)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", itoaStr(pd.ID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	h.ProviderDelete(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	defs, _ := h.store.GetProviderDefs()
	for _, d := range defs {
		if d.Name == "delprov" {
			t.Error("provider should be deleted")
		}
	}
}

func TestProviderDelete_MissingID(t *testing.T) {
	h, _ := newScriptedWebui(t)
	sess := testSession(t, h)

	rec := httptest.NewRecorder()
	req := authReq2(t, http.MethodDelete, "/admin/providers/", sess)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	h.ProviderDelete(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestChannelFetchModels_NoSuchChannel(t *testing.T) {
	h, _ := newScriptedWebui(t)
	sess := testSession(t, h)

	rec := httptest.NewRecorder()
	req := authReq2(t, http.MethodGet, "/admin/channels/999/models", sess)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "999")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	h.ChannelFetchModels(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestChannelFetchModels_NoKey(t *testing.T) {
	h, _ := newScriptedWebui(t)
	sess := testSession(t, h)

	ch := &model.Channel{
		Name: "nokey-ch", Provider: "openai", Protocol: "openai",
		BaseURL: "https://api.openai.com/v1", Models: []string{"gpt-4o"},
		Status: model.ChannelEnabled,
	}
	if err := h.store.CreateChannel(ch); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	rec := httptest.NewRecorder()
	req := authReq2(t, http.MethodGet, "/admin/channels/"+itoaStr(ch.ID)+"/models", sess)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", itoaStr(ch.ID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	h.ChannelFetchModels(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 (no key), got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestChannelFetchModels_Success(t *testing.T) {
	h, _ := newScriptedWebui(t)
	sess := testSession(t, h)

	ch := &model.Channel{
		Name: "fm-ch", Provider: "openai", Protocol: "openai",
		BaseURL: "https://api.openai.com/v1", Models: []string{},
		Status: model.ChannelEnabled,
	}
	if err := h.store.CreateChannel(ch); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if err := h.store.CreateKey(&model.Key{
		ChannelID: ch.ID, Key: "sk-test", KeyMasked: "sk**est", Status: model.KeyActive,
	}); err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	mp := &fetchMockProvider{models: []string{"gpt-4o", "gpt-4o-mini"}}
	provider.SetFactoryOverride(mp)
	defer provider.SetFactoryOverride(nil)

	rec := httptest.NewRecorder()
	req := authReq2(t, http.MethodGet, "/admin/channels/"+itoaStr(ch.ID)+"/models", sess)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", itoaStr(ch.ID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	h.ChannelFetchModels(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "gpt-4o") {
		t.Error("response should contain gpt-4o")
	}
}

type fetchMockProvider struct {
	models []string
}

func (f *fetchMockProvider) Name() string { return "mock" }
func (f *fetchMockProvider) Chat(_ context.Context, _ *provider.ChatRequest, _, _ string) (*provider.ChatResponse, int, error) {
	return &provider.ChatResponse{}, 200, nil
}
func (f *fetchMockProvider) ListModels(_ context.Context, _, _ string) ([]string, error) {
	return f.models, nil
}

func itoaStr(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestModelsByProvider_OpenAI(t *testing.T) {
	modelmeta.Init("")
	h, _ := newScriptedWebui(t)
	sess := testSession(t, h)

	rec := httptest.NewRecorder()
	req := authReq2(t, http.MethodGet, "/admin/api/models-by-provider?provider=openai", sess)

	h.ModelsByProvider(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "gpt-4o") {
		t.Error("response should contain gpt-4o")
	}
	if !strings.Contains(rec.Body.String(), "context_window") {
		t.Error("response should contain context_window field")
	}
}

func TestModelsByProvider_EmptyProvider(t *testing.T) {
	h, _ := newScriptedWebui(t)
	sess := testSession(t, h)

	rec := httptest.NewRecorder()
	req := authReq2(t, http.MethodGet, "/admin/api/models-by-provider", sess)

	h.ModelsByProvider(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"models":[]`) && !strings.Contains(body, `"models":null`) {
		t.Errorf("expected empty models array, got %s", body)
	}
}

func TestModelsByProvider_UnknownProvider(t *testing.T) {
	h, _ := newScriptedWebui(t)
	sess := testSession(t, h)

	rec := httptest.NewRecorder()
	req := authReq2(t, http.MethodGet, "/admin/api/models-by-provider?provider=totallymadeup", sess)

	h.ModelsByProvider(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"models":[]`) {
		t.Errorf("expected empty models array for unknown provider, got %s", rec.Body.String())
	}
}

func TestMetaProviders(t *testing.T) {
	modelmeta.Init("")
	h, _ := newScriptedWebui(t)
	sess := testSession(t, h)

	rec := httptest.NewRecorder()
	req := authReq2(t, http.MethodGet, "/admin/api/meta-providers", sess)

	h.MetaProviders(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"providers"`) {
		t.Error("response should contain providers field")
	}
	if !strings.Contains(rec.Body.String(), "openai") {
		t.Error("response should contain openai provider")
	}
}

func TestAvailableModels(t *testing.T) {
	modelmeta.Init("")
	h, _ := newScriptedWebui(t)
	sess := testSession(t, h)

	rec := httptest.NewRecorder()
	req := authReq2(t, http.MethodGet, "/admin/api/available-models", sess)

	h.AvailableModels(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"models"`) {
		t.Error("response should contain models field")
	}
}
