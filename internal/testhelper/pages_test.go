package testhelper

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/sn0wfree/llmRx/internal/webui"
)

// newWebUIForDump builds the web console handler against the app's
// store — the same wiring server.New uses (store, logstore,
// admin-API bridge, config path). Config path is empty: route walking
// never renders the YAML editor.
func newWebUIForDump(app *App) (*webui.Handler, error) {
	return webui.New(app.Store, app.LogStore, webui.NewWebAPIBridge(app.Store), "")
}

// dumpMux walks one chi mux and returns sorted "METHOD /path" lines.
func dumpMux(t *testing.T, mux *chi.Mux) []string {
	t.Helper()
	seen := map[string]bool{}
	var lines []string
	err := chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == "" || method == "*" {
			return nil
		}
		route = strings.TrimSuffix(route, "/")
		if route == "" {
			route = "/"
		}
		key := method + " " + route
		if !seen[key] {
			seen[key] = true
			lines = append(lines, key)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(lines)
	return lines
}

// TestDumpWebPages walks the web console's two route groups (admin
// pages + user self-service pages). Run with DUMP_ROUTES=1.
func TestDumpWebPages(t *testing.T) {
	if os.Getenv("DUMP_ROUTES") == "" {
		t.Skip("set DUMP_ROUTES=1 to print the route table")
	}
	app := New(t)
	w, err := newWebUIForDump(app)
	if err != nil {
		t.Fatalf("webui: %v", err)
	}
	adminMux, okA := w.Routes().(*chi.Mux)
	userMux, okU := w.UserRoutes().(*chi.Mux)
	if !okA || !okU {
		t.Fatalf("webui routes are %T/%T", w.Routes(), w.UserRoutes())
	}
	var lines []string
	for _, l := range dumpMux(t, adminMux) {
		lines = append(lines, "PAGE /admin"+l)
	}
	for _, l := range dumpMux(t, userMux) {
		lines = append(lines, "USER /account"+l)
	}
	sort.Strings(lines)
	for _, l := range lines {
		t.Logf("%s", l)
	}
	t.Logf("TOTAL %d page routes", len(lines))
}

// TestAdminPageRoutes_Contract pins the v0.2.0 admin console page
// rename: old URLs must 404 (no route), new URLs must be reachable
// (login redirect without a session — not 404). This is the
// regression net for the rename documented in UPGRADE.md #10.
func TestAdminPageRoutes_Contract(t *testing.T) {
	app := New(t)
	w, err := newWebUIForDump(app)
	if err != nil {
		t.Fatalf("webui: %v", err)
	}
	adminMux, ok := w.Routes().(*chi.Mux)
	if !ok {
		t.Fatalf("webui routes are %T", w.Routes())
	}

	// Routes() registers page paths WITHOUT the /admin mount prefix
	// (server.go mounts the group at /admin) — so contract requests
	// use the bare paths.
	legacy := map[string]int{}
	for _, p := range []string{
		"/usage",
		"/monitoring",
		"/subscriptions",
	} {
		legacy[p] = doRequest(adminMux, http.MethodGet, p)
	}
	for p, code := range legacy {
		if code != http.StatusNotFound {
			t.Errorf("legacy path %s = %d, want 404 (route must not exist)", p, code)
		}
	}

	current := map[string]int{}
	for _, p := range []string{
		"/user-usage",
		"/channel-monitor",
		"/subscription-quota",
	} {
		current[p] = doRequest(adminMux, http.MethodGet, p)
	}
	for p, code := range current {
		if code == http.StatusNotFound {
			t.Errorf("current path %s = 404 — route missing!", p)
		}
	}
}

// doRequest fires one request against the mux and returns the status.
func doRequest(h http.Handler, method, path string) int {
	req, _ := http.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}
