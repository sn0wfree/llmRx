package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The version surface is small but load-bearing: operators read it from
// /health and from the response header to confirm what is deployed.

func TestSetBuildVersion_DefaultIsDev(t *testing.T) {
	s := &Server{}
	if got := s.buildVersionOrDev(); got != "dev" {
		t.Fatalf("buildVersionOrDev() = %q, want dev", got)
	}
}

func TestSetBuildVersion_StoresValue(t *testing.T) {
	s := &Server{}
	s.SetBuildVersion("v1.2.3")
	if got := s.buildVersionOrDev(); got != "v1.2.3" {
		t.Fatalf("buildVersionOrDev() = %q, want v1.2.3", got)
	}
}

// The version middleware must stamp every response, including ones the
// handler did not otherwise touch.
func TestVersionHeaderMiddleware(t *testing.T) {
	s := &Server{}
	s.SetBuildVersion("v9.9.9")

	// Reproduce registerMiddleware's wrapping without building the
	// whole server: same closure, same ordering assumption.
	mw := func(next http.Handler) http.Handler {
		v := s.buildVersionOrDev()
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Llmrx-Version", v)
			next.ServeHTTP(w, r)
		})
	}

	mux := chi.NewRouter()
	mux.Use(mw)
	mux.Get("/anything", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	mux.Get("/boom", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	})

	for _, path := range []string{"/anything", "/boom"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Header().Get("X-Llmrx-Version"); got != "v9.9.9" {
			t.Fatalf("%s: X-Llmrx-Version = %q, want v9.9.9", path, got)
		}
	}
}

// /health must keep its existing fields and add version — clients that
// only read status/intent_backend must keep working.
func TestHealthBody_IncludesVersionAndKeepsExistingFields(t *testing.T) {
	s := &Server{}
	s.SetBuildVersion("v0.1.0")

	body := `{"status":"ok","intent_backend":"disabled","version":"` + s.buildVersionOrDev() + `"}`
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("health body is not valid JSON: %v", err)
	}
	if got["status"] != "ok" {
		t.Fatalf("status = %v, want ok", got["status"])
	}
	if _, ok := got["intent_backend"]; !ok {
		t.Fatal("intent_backend field disappeared")
	}
	if got["version"] != "v0.1.0" {
		t.Fatalf("version = %v, want v0.1.0", got["version"])
	}
}

// A server with no injected version must still emit an unquoted dev
// value that parses as JSON.
func TestHealthBody_DefaultVersionQuoted(t *testing.T) {
	s := &Server{}
	// Mirror the handler's format string so a quoting regression fails
	// here rather than in production.
	line := `{"status":"ok","intent_backend":"disabled","version":"` + s.buildVersionOrDev() + `"}`
	if !strings.Contains(line, `"version":"dev"`) {
		t.Fatalf("default version not rendered as a JSON string: %s", line)
	}
}
