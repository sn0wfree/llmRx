package requestid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestGenerate_Format(t *testing.T) {
	id := generate()
	if len(id) != 16 {
		t.Fatalf("len: got %d, want 16", len(id))
	}
	if matched, _ := regexp.MatchString("^[0-9a-f]{16}$", id); !matched {
		t.Fatalf("not hex16: %q", id)
	}
}

func TestGenerate_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := generate()
		if seen[id] {
			t.Fatalf("duplicate id: %s", id)
		}
		seen[id] = true
	}
}

func TestFromContext(t *testing.T) {
	ctx := NewContext(context.Background(), "abc-123")
	if got := FromContext(ctx); got != "abc-123" {
		t.Errorf("got %q", got)
	}
	if got := FromContext(context.Background()); got != "" {
		t.Errorf("empty context: got %q", got)
	}
}

func TestMiddleware_GeneratesID(t *testing.T) {
	var seen string
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context())
	}))
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if seen == "" {
		t.Fatal("context missing request ID")
	}
	if len(seen) != 16 {
		t.Errorf("len: got %d", len(seen))
	}
	if w.Header().Get(HeaderName) != seen {
		t.Errorf("response header mismatch: %q vs %q", w.Header().Get(HeaderName), seen)
	}
}

func TestMiddleware_ReusesIncomingID(t *testing.T) {
	var seen string
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context())
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(HeaderName, "upstream-abc-123")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if seen != "upstream-abc-123" {
		t.Errorf("expected reuse, got %q", seen)
	}
	if w.Header().Get(HeaderName) != "upstream-abc-123" {
		t.Errorf("response should echo upstream id, got %q", w.Header().Get(HeaderName))
	}
}

func TestOrNew(t *testing.T) {
	ctx, id1 := OrNew(context.Background())
	if id1 == "" {
		t.Fatal("generated id should not be empty")
	}
	if FromContext(ctx) != id1 {
		t.Error("ctx missing id")
	}

	_, id2 := OrNew(ctx)
	if id2 != id1 {
		t.Errorf("OrNew should reuse existing id: %q vs %q", id1, id2)
	}
}

func TestHeaderName(t *testing.T) {
	if strings.ToLower(HeaderName) != "x-request-id" {
		t.Errorf("unexpected header name: %q", HeaderName)
	}
}