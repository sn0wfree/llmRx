package api

import (
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