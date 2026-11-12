package testhelper

import (
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestDumpRouteTable walks the assembled API mux and prints the
// complete route table (method + path), sorted. This is the
// authoritative inventory for docs/ROUTES.md — grep-based lists drift.
// Web console pages are walked separately (TestDumpWebPages).
//
// Run: DUMP_ROUTES=1 go test -run 'TestDump' -v ./internal/testhelper/
func TestDumpRouteTable(t *testing.T) {
	if os.Getenv("DUMP_ROUTES") == "" {
		t.Skip("set DUMP_ROUTES=1 to print the route table")
	}
	app := New(t)
	mux, ok := app.Mux.(*chi.Mux)
	if !ok {
		t.Fatalf("Mux is %T, want *chi.Mux", app.Mux)
	}
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
	for _, l := range lines {
		t.Logf("%s", l)
	}
	t.Logf("TOTAL %d routes", len(lines))
}
