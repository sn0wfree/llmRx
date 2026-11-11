package webui

import (
	"testing"
	"time"
)

// TestUsageRange covers the ?range= filter windows, including the
// unlisted-but-supported 90d window and the "all time" default of 0.
func TestUsageRange(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want int64
	}{
		{"24h", now.Add(-24 * time.Hour).Unix()},
		{"7d", now.Add(-7 * 24 * time.Hour).Unix()},
		{"30d", now.Add(-30 * 24 * time.Hour).Unix()},
		{"90d", now.Add(-90 * 24 * time.Hour).Unix()},
		{"", 0},
		{"bogus", 0},
	}
	for _, tc := range cases {
		if got := usageRange(now, tc.in); got != tc.want {
			t.Errorf("usageRange(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
