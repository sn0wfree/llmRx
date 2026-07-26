package provider

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func readBody(r *http.Request) []byte {
	b, _ := io.ReadAll(r.Body)
	return b
}

func assertJSONContains(t *testing.T, body []byte, key string) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v\n%s", err, body)
	}
	if _, ok := got[key]; !ok {
		t.Errorf("expected key %q in body, got: %s", key, string(body))
	}
}

func assertStrContains(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("expected %q in %s", sub, s)
	}
}
