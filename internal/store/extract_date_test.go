package store

import "testing"

// extractDateForStore pulls the YYYY-MM-DD prefix from a day file
// basename. For "2026-07-09" (base file) it returns "2026-07-09".
// For "2026-07-09-2" (rollover file) it returns "2026-07-09".
// Non-date basenames (e.g. "runtime_settings") pass through.
func TestExtractDateForStore_BaseDate(t *testing.T) {
	if got := extractDateForStore("2026-07-09"); got != "2026-07-09" {
		t.Errorf("got %q, want 2026-07-09", got)
	}
}

func TestExtractDateForStore_Rollover(t *testing.T) {
	if got := extractDateForStore("2026-07-09-2"); got != "2026-07-09" {
		t.Errorf("got %q, want 2026-07-09", got)
	}
}

func TestExtractDateForStore_LargeRollover(t *testing.T) {
	// 12-char filename (YYYY-MM-DD-N where N is up to 99) should
	// also strip to YYYY-MM-DD.
	if got := extractDateForStore("2026-07-09-99"); got != "2026-07-09" {
		t.Errorf("got %q, want 2026-07-09", got)
	}
}

func TestExtractDateForStore_NonDatePassThrough(t *testing.T) {
	// Files that don't start with a YYYY-MM-DD prefix (e.g. the
	// runtime_settings key) must round-trip unchanged.
	tests := []string{
		"runtime_settings",
		"alerts",
		"foo",
		"2026-7-9",   // not zero-padded — not a valid YYYY-MM-DD
		"2026-13-01", // month 13 — invalid
		"abcd-ef-gh",
	}
	for _, in := range tests {
		if got := extractDateForStore(in); got != in {
			t.Errorf("extractDateForStore(%q) = %q, want passthrough", in, got)
		}
	}
}

func TestExtractDateForStore_ShortInput(t *testing.T) {
	// Inputs shorter than YYYY-MM-DD pass through unchanged.
	tests := []string{"", "x", "2026", "2026-07", "2026-07-0"}
	for _, in := range tests {
		if got := extractDateForStore(in); got != in {
			t.Errorf("extractDateForStore(%q) = %q, want passthrough", in, got)
		}
	}
}
