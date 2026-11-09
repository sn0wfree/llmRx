package subusage

import (
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// SourceRatio returns the vendor-reported consumed fraction for the
// channel's most constrained live window, so the router honours quota
// burned by other tools sharing the same upstream account.
//
// It returns ok=false — meaning "fall back to the local count" — when
// there is no probe, no cached report, or every window the vendor
// reported has already reset. A stale report is worse than none: it
// would keep a channel sidelined after its window rolled over.
//
// This lives here rather than in internal/usage because it reads the
// vendor report format, which this package owns; the local counter
// (internal/usage) must not depend on the vendor side.
func SourceRatio(ch *model.Channel, now time.Time) (float64, bool) {
	if !ch.UsageProbe.Enabled() || ch.UsageState == "" {
		return 0, false
	}
	rep := ReportFromState(ch.UsageState)
	if rep.Source == "" || len(rep.Points) == 0 {
		return 0, false
	}

	tightest := -1.0
	live := false
	for i := range rep.Points {
		p := rep.Points[i]
		// Skip windows the vendor says have rolled over.
		if !p.ResetsAt.IsZero() && !p.ResetsAt.After(now) {
			continue
		}
		live = true
		if p.Limit <= 0 {
			continue
		}
		if r := p.Ratio(); r > tightest {
			tightest = r
		}
	}
	if !live {
		return 0, false
	}
	if tightest < 0 {
		// The vendor reports usage but no limits at all: nothing to
		// enforce.
		return 0, false
	}
	return tightest, true
}

// RatioSource adapts the package-level SourceRatio to the router's
// VendorUsageProvider interface. It is stateless — the reading comes
// from the report cached on the channel — so it can be wired into the
// router even when no monitor is running.
type RatioSource struct{}

// SourceRatio implements the router's vendor-side contract.
func (RatioSource) SourceRatio(ch *model.Channel, now time.Time) (float64, bool) {
	return SourceRatio(ch, now)
}

// SourceRatio makes *Monitor satisfy the router's vendor-side
// contract, so the wiring can pass the monitor itself.
func (m *Monitor) SourceRatio(ch *model.Channel, now time.Time) (float64, bool) {
	return SourceRatio(ch, now)
}
