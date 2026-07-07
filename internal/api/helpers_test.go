package api

import (
	"math"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
)

func TestCalcCost(t *testing.T) {
	ch := &model.Channel{InputPrice: 1.0, OutputPrice: 3.0}
	usage := provider.Usage{PromptTokens: 1_000_000, CompletionTokens: 500_000}
	got := calcCost(ch, usage)
	want := 1.0*1 + 3.0*0.5 // 1 + 1.5 = 2.5
	if got != want {
		t.Fatalf("calcCost: expected %.4f, got %.4f", want, got)
	}
}

func TestCalcCostZero(t *testing.T) {
	ch := &model.Channel{InputPrice: 0.5, OutputPrice: 2.0}
	usage := provider.Usage{}
	if got := calcCost(ch, usage); got != 0 {
		t.Fatalf("calcCost zero: got %.4f", got)
	}
}

// TestCalcCost_CachedTokensDiscount verifies that prompt-cache hits
// are billed at CachedInputDiscount * InputPrice rather than the
// full InputPrice. Without a discount, all tokens still cost the
// full rate (i.e., no double-billing).
func TestCalcCost_CachedTokensDiscount(t *testing.T) {
	cases := []struct {
		name       string
		channel    model.Channel
		prompt     int
		cached     int
		completion int
		want       float64
	}{
		{
			name:    "anthropic-10pct-discount-50pct-cached",
			channel: model.Channel{InputPrice: 3.0, OutputPrice: 15.0, CachedInputDiscount: 0.1},
			prompt:  1_000_000, cached: 500_000, completion: 1_000_000,
			// normal: 0.5M @ $3 = $1.50
			// cached: 0.5M @ $3 * 0.1 = $0.15
			// output: 1M @ $15 = $15
			// total: $16.65
			want: 16.65,
		},
		{
			name:    "zero-discount-no-savings",
			channel: model.Channel{InputPrice: 2.0, OutputPrice: 6.0, CachedInputDiscount: 0},
			prompt:  1_000_000, cached: 800_000, completion: 0,
			// normal: 200K @ $2 = $0.40; cached: $0 (no discount)
			want: 0.4,
		},
		{
			name:    "zero-discount-cached-tokens-free",
			channel: model.Channel{InputPrice: 3.0, OutputPrice: 15.0, CachedInputDiscount: 0.0},
			prompt:  1_000_000, cached: 1_000_000, completion: 1_000_000,
			// all prompt cached @ 0% → free; output = 1M @ $15 = $15
			want: 15.0,
		},
		{
			name:    "full-discount-no-savings",
			channel: model.Channel{InputPrice: 3.0, OutputPrice: 15.0, CachedInputDiscount: 1.0},
			prompt:  1_000_000, cached: 500_000, completion: 1_000_000,
			// normal: 0.5M @ $3 = $1.50
			// cached: 0.5M @ $3 * 1.0 = $1.50 (full rate)
			// output: 1M @ $15 = $15
			want: 18.0,
		},
		{
			name:    "cached-exceeds-prompt-clamped",
			channel: model.Channel{InputPrice: 1.0, OutputPrice: 2.0, CachedInputDiscount: 0.1},
			prompt:  100, cached: 500, completion: 0,
			// pathological upstream: cached > prompt; clamp cached to prompt
			// → all 100 prompt cached @ $1 * 0.1 = $0.00001
			want: 0.00001,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			usage := provider.Usage{
				PromptTokens:     tc.prompt,
				CompletionTokens: tc.completion,
				PromptTokensDetails: &provider.PromptTokensDetails{CachedTokens: tc.cached},
			}
			ch := tc.channel
			got := calcCost(&ch, usage)
			if math.Abs(got-tc.want) > 1e-6 {
				t.Fatalf("got %.6f, want %.6f", got, tc.want)
			}
		})
	}
}

func TestErrorTypeFor(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{400, "invalid_request_error"},
		{401, "invalid_request_error"},
		{403, "invalid_request_error"},
		{404, "invalid_request_error"},
		{500, "api_error"},
		{503, "api_error"},
		{200, "upstream_error"},
		{429, "upstream_error"},
	}
	for _, tc := range cases {
		if got := errorTypeFor(tc.status); got != tc.want {
			t.Fatalf("errorTypeFor(%d): expected %q, got %q", tc.status, tc.want, got)
		}
	}
}