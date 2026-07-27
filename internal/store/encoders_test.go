package store

import (
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

// --- applyPragmas ---

func TestApplyPragmas_OK(t *testing.T) {
	s := openTemp(t)
	// applyPragmas was called by OpenSQLite; we just need to verify
	// the pragmas are present in the connection. cache_size and
	// journal_mode are the most useful to spot-check.
	var cacheSize, journalMode string
	if err := s.db.QueryRow("PRAGMA cache_size").Scan(&cacheSize); err != nil {
		t.Fatalf("cache_size: %v", err)
	}
	if cacheSize == "0" || cacheSize == "" {
		t.Errorf("expected non-default cache_size, got %q", cacheSize)
	}
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	// The default is "delete"; OpenSQLite may leave it as "delete" since
	// we don't override journal_mode (WAL is only enabled via DSN).
	if journalMode == "" {
		t.Errorf("expected non-empty journal_mode, got %q", journalMode)
	}
}

// --- decodeStrings / decodeCB ---

func TestDecodeStrings_Empty(t *testing.T) {
	if got := decodeStrings(""); got != nil {
		t.Errorf("empty input should return nil, got %v", got)
	}
}

func TestDecodeStrings_Valid(t *testing.T) {
	got := decodeStrings(`["a","b","c"]`)
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("got %v, want [a b c]", got)
	}
}

func TestDecodeStrings_InvalidJSON(t *testing.T) {
	// Invalid JSON returns nil from json.Unmarshal; the helper
	// swallows the error so the caller sees a nil slice.
	got := decodeStrings("not json")
	if got != nil {
		t.Errorf("invalid JSON should return nil, got %v", got)
	}
}

func TestDecodeStrings_EmptyArray(t *testing.T) {
	got := decodeStrings("[]")
	if got == nil {
		t.Errorf("[] should decode to non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("len=%d, want 0", len(got))
	}
}

func TestDecodeCB_Empty(t *testing.T) {
	cb := decodeCB("")
	if cb.MaxFailures != 0 || cb.ResetTimeout != 0 {
		t.Errorf("empty input should give zero config, got %+v", cb)
	}
}

func TestDecodeCB_Valid(t *testing.T) {
	cb := decodeCB(`{"max_failures":5,"reset_timeout":30000}`)
	if cb.MaxFailures != 5 {
		t.Errorf("MaxFailures = %d, want 5", cb.MaxFailures)
	}
	if cb.ResetTimeout != 30000 {
		t.Errorf("ResetTimeout = %v, want 30000", cb.ResetTimeout)
	}
}

func TestDecodeCB_InvalidJSON(t *testing.T) {
	// Invalid JSON returns zero value (error is swallowed).
	cb := decodeCB("not json")
	if cb.MaxFailures != 0 || cb.ResetTimeout != 0 {
		t.Errorf("invalid JSON should return zero, got %+v", cb)
	}
}

// --- encodeStrings / encodeCB round-trips ---

func TestEncodeDecodeStrings_RoundTrip(t *testing.T) {
	in := []string{"foo", "bar", "baz with space"}
	enc := encodeStrings(in)
	dec := decodeStrings(enc)
	if len(dec) != len(in) {
		t.Fatalf("len mismatch: got %d, want %d", len(dec), len(in))
	}
	for i := range in {
		if dec[i] != in[i] {
			t.Errorf("dec[%d] = %q, want %q", i, dec[i], in[i])
		}
	}
}

func TestEncodeDecodeCB_RoundTrip(t *testing.T) {
	in := model.CircuitBreakerConfig{MaxFailures: 3, ResetTimeout: 1000}
	enc := encodeCB(in)
	dec := decodeCB(enc)
	if dec != in {
		t.Errorf("roundtrip mismatch: got %+v, want %+v", dec, in)
	}
}
