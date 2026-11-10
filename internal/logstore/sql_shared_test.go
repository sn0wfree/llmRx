package logstore

import (
	"fmt"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// TestClampPage pins the shared pagination normaliser used by both
// drivers' QueryAcross: default page 50, hard cap 500, offset floored
// at 0, valid input untouched.
func TestClampPage(t *testing.T) {
	cases := []struct{ limit, offset, wantL, wantO int }{
		{0, 0, 50, 0},      // unset limit → default page
		{-1, -10, 50, 0},   // negatives normalised
		{600, 5, 500, 5},   // hard cap at 500
		{25, 120, 25, 120}, // valid passthrough
	}
	for _, c := range cases {
		gotL, gotO := clampPage(c.limit, c.offset)
		if gotL != c.wantL || gotO != c.wantO {
			t.Errorf("clampPage(%d, %d) = (%d, %d), want (%d, %d)",
				c.limit, c.offset, gotL, gotO, c.wantL, c.wantO)
		}
	}
}

// TestLogInsertArgs_ColumnOrder pins each bound value to its column
// position in logsInsertSQL. A mismatch is a silent data corruption
// (tokens recorded as costs, etc.), so every position is asserted.
func TestLogInsertArgs_ColumnOrder(t *testing.T) {
	e := &model.Log{
		TokenID:         11,
		ChannelID:       22,
		KeyID:           33,
		Model:           "gpt-x",
		PromptTokens:    44,
		CompletionTokens: 55,
		CachedTokens:    66,
		ReasoningTokens: 77,
		CacheHit:        true,
		RealCostUSD:     0.5,
		BilledCostUSD:   1.25,
		DurationMs:      88,
		StatusCode:      200,
		RouterPath:      "L1-L5",
		RequestIP:       "10.0.0.1",
		Endpoint:        "chat",
		Units:           9,
		CreatedAt:       time.Unix(1700000123, 0),
	}
	args := logInsertArgs(e)
	want := []any{
		11, 22, 33, "gpt-x",
		44, 55, 66, 77,
		1, // cache_hit via boolToInt
		0.5, 1.25,
		88, 200, "L1-L5", "10.0.0.1", "chat", 9,
		1700000123, // created_at unix seconds (UTC)
	}
	if len(args) != len(want) {
		t.Fatalf("logInsertArgs binds %d values, want %d", len(args), len(want))
	}
	for i := range want {
		if fmt.Sprint(args[i]) != fmt.Sprint(want[i]) {
			t.Errorf("arg[%d] = %v, want %v", i, args[i], want[i])
		}
	}
}
