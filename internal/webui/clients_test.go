package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// fakeEdgeControl implements EdgeControl with canned data so the page's
// wiring (routing, rendering states, form handling) can be tested
// without a clientapi.Ingestor — which would drag in the log store.
type fakeEdgeControl struct {
	clients     []map[string]any
	bundle      any
	issuedToken string
	issuedFor   string
	issueErr    error
	pushed      json.RawMessage
	pushVersion int
	pushErr     error
	revoked     string
}

func (f *fakeEdgeControl) EdgeClientsStatusJSON() any { return f.clients }
func (f *fakeEdgeControl) CurrentBundleJSON() any     { return f.bundle }
func (f *fakeEdgeControl) PushBundleJSON(raw json.RawMessage) (int, error) {
	if f.pushErr != nil {
		return 0, f.pushErr
	}
	f.pushed = raw
	if f.pushVersion == 0 {
		f.pushVersion = 2
	}
	return f.pushVersion, nil
}
func (f *fakeEdgeControl) IssueEnrollTokenJSON(name string) (any, error) {
	if f.issueErr != nil {
		return nil, f.issueErr
	}
	f.issuedFor = name
	return map[string]any{
		"token":      f.issuedToken,
		"expires_at": "2026-11-01T12:15:00Z",
		"name":       name,
		"notice":     "one shot",
	}, nil
}
func (f *fakeEdgeControl) RevokeEdgeClientJSON(id string) error {
	f.revoked = id
	return nil
}

func newClientsHandler(t *testing.T, ec EdgeControl) *Handler {
	t.Helper()
	h, _ := newTestWebUI(t)
	if ec != nil {
		h.SetEdgeControl(ec)
	}
	return h
}

// WebuiStore is satisfied by the handler's real store; only the render
// path matters here.

func TestClientsPage_NotConfigured(t *testing.T) {
	h := newClientsHandler(t, nil)

	rec := httptest.NewRecorder()
	h.ClientsPage(rec, httptest.NewRequest(http.MethodGet, "/admin/clients", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "未配置客户端") {
		t.Fatalf("expected the guidance panel, got: %s", trim(body, 400))
	}
	// And it must show how to fix it rather than just erroring.
	if !strings.Contains(body, "edge_clients") {
		t.Fatal("guidance does not mention the config key")
	}
}

func TestClientsPage_RendersStates(t *testing.T) {
	ec := &fakeEdgeControl{
		clients: []map[string]any{
			{"name": "online-one", "client_id": "ec_a", "online": true, "client_version": "v1.2.3",
				"last_seen": "2026-11-01T10:00:00Z"},
			{"name": "offline-one", "client_id": "ec_b", "online": false, "last_seen": "2026-10-30T10:00:00Z"},
			{"name": "revoked-one", "client_id": "ec_c", "revoked": true, "online": true},
			{"name": "static-one"}, // config.yml bootstrap entry: no id
		},
		bundle: map[string]any{"version": 1, "cache": map[string]any{"enabled": true}},
	}
	h := newClientsHandler(t, ec)

	rec := httptest.NewRecorder()
	h.ClientsPage(rec, httptest.NewRequest(http.MethodGet, "/admin/clients", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{"online-one", "offline-one", "revoked-one", "static-one",
		"在线", "离线", "已吊销", "v1.2.3", "签发接入码", "配置下发"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q", want)
		}
	}

	// A revoked client must never render as online, even if the control
	// plane reports a recent heartbeat.
	idx := strings.Index(body, "revoked-one")
	if idx < 0 {
		t.Fatal("revoked client missing")
	}
	row := body[idx:]
	if next := strings.Index(row, "</tr>"); next > 0 {
		row = row[:next]
	}
	if strings.Contains(row, "在线") {
		t.Fatal("a revoked client rendered as online")
	}
}

func TestClientsPage_ShowsIssuedTokenOnce(t *testing.T) {
	ec := &fakeEdgeControl{clients: nil, bundle: map[string]any{}}
	h := newClientsHandler(t, ec)

	req := httptest.NewRequest(http.MethodGet,
		"/admin/clients?enroll_token=enr_abc123&expires_at=2026-11-01T12:15:00Z", nil)
	rec := httptest.NewRecorder()
	h.ClientsPage(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "enr_abc123") {
		t.Fatal("issued token not displayed")
	}
	if !strings.Contains(body, "只显示这一次") {
		t.Fatal("missing the one-shot warning")
	}
	if !strings.Contains(body, "gateway-client -enroll") {
		t.Fatal("missing the usage hint")
	}
}

func TestClientsEnroll_RedirectsWithToken(t *testing.T) {
	ec := &fakeEdgeControl{issuedToken: "enr_new_token", bundle: map[string]any{}}
	h := newClientsHandler(t, ec)

	form := url.Values{"name": {"alice-laptop"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/clients/enroll", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ClientsEnroll(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "enroll_token=enr_new_token") {
		t.Fatalf("Location = %q, want the token", loc)
	}
	if ec.issuedFor != "alice-laptop" {
		t.Fatalf("issued for %q", ec.issuedFor)
	}
}

func TestClientsEnroll_ErrorSurfaces(t *testing.T) {
	ec := &fakeEdgeControl{issueErr: errBad("client name is required")}
	h := newClientsHandler(t, ec)

	form := url.Values{"name": {""}}
	req := httptest.NewRequest(http.MethodPost, "/admin/clients/enroll", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ClientsEnroll(rec, req)

	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "error=") {
		t.Fatalf("Location = %q, want an error redirect", loc)
	}
}

func TestClientsPushConfig_Pushes(t *testing.T) {
	ec := &fakeEdgeControl{bundle: map[string]any{}}
	h := newClientsHandler(t, ec)

	bundle := `{"cache":{"enabled":false},"fallback":{"mode":"local"}}`
	form := url.Values{"bundle": {bundle}}
	req := httptest.NewRequest(http.MethodPost, "/admin/clients/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ClientsPushConfig(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "flash=") {
		t.Fatalf("Location = %q, want a success flash", loc)
	}
	if string(ec.pushed) != bundle {
		t.Fatalf("pushed %q, want %q", string(ec.pushed), bundle)
	}
}

func TestClientsPushConfig_RejectsEmpty(t *testing.T) {
	ec := &fakeEdgeControl{}
	h := newClientsHandler(t, ec)

	form := url.Values{"bundle": {"   "}}
	req := httptest.NewRequest(http.MethodPost, "/admin/clients/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ClientsPushConfig(rec, req)

	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "error=") {
		t.Fatalf("Location = %q, want an error redirect", loc)
	}
	if ec.pushed != nil {
		t.Fatal("an empty bundle must not be pushed")
	}
}

func TestClientsPushConfig_InvalidJSONSurfaces(t *testing.T) {
	ec := &fakeEdgeControl{bundle: map[string]any{}, pushErr: errBad("invalid bundle: unexpected end of JSON input")}
	h := newClientsHandler(t, ec)

	form := url.Values{"bundle": {`{nope`}}
	req := httptest.NewRequest(http.MethodPost, "/admin/clients/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ClientsPushConfig(rec, req)

	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "error=") {
		t.Fatalf("Location = %q, want an error redirect", loc)
	}
}

func TestClientsRevoke_Redirects(t *testing.T) {
	ec := &fakeEdgeControl{}
	h := newClientsHandler(t, ec)

	// Route through a real chi router so the URL parameter lands in the
	// context the way it does in production.
	r := chi.NewRouter()
	r.Post("/admin/clients/{id}/revoke", h.ClientsRevoke)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/clients/ec_abc/revoke", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	if ec.revoked != "ec_abc" {
		t.Fatalf("revoked %q", ec.revoked)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "flash=") {
		t.Fatalf("Location = %q", loc)
	}
}

func TestClientsPage_AllActionsRequireEnabledControl(t *testing.T) {
	h := newClientsHandler(t, nil)

	for _, tc := range []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
		path string
	}{
		{"enroll", h.ClientsEnroll, "/admin/clients/enroll"},
		{"config", h.ClientsPushConfig, "/admin/clients/config"},
		{"revoke", h.ClientsRevoke, "/admin/clients/ec_x/revoke"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader("name=x&bundle={}"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			tc.fn(rec, req)
			if loc := rec.Header().Get("Location"); !strings.Contains(loc, "error=") {
				t.Fatalf("Location = %q, want an error redirect when unconfigured", loc)
			}
		})
	}
}

// ---------- helpers ----------

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
