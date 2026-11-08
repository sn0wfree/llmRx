package usage

import (
	"time"

	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

// RecoverRecent replays logged rows into the tracker so plan-quota
// windows survive a restart. The replay starts at the widest window
// boundary among all tracked channels — start of the current month
// for monthly quotas, start of the current UTC day for daily, now
// minus the rolling hours for rolling ones. Deployments without any
// tracked channel replay nothing; windows older than the logstore
// retention horizon simply recover partially (the logs are the
// source of truth, whatever survived them is what we know).
func RecoverRecent(t *Tracker, ls *logstore.Manager, st store.Store) {
	if t == nil || ls == nil || st == nil {
		return
	}
	chs, err := st.GetChannels()
	if err != nil {
		logging.Warn("usage recovery: list channels failed", logging.F("error", err.Error()))
		return
	}
	now := time.Now()
	var since time.Time
	for i := range chs {
		q := chs[i].PlanQuota
		if !q.Tracked() {
			continue
		}
		start, _ := WindowRange(q, now)
		if start.IsZero() {
			continue
		}
		if since.IsZero() || start.Before(since) {
			since = start
		}
	}
	if since.IsZero() {
		return
	}
	rows := 0
	err = ls.ScanSince(since, func(e *model.Log) bool {
		t.Observe(e.ChannelID, e.PromptTokens, e.CompletionTokens, e.CachedTokens,
			e.ReasoningTokens, e.RealCostUSD, e.CreatedAt)
		rows++
		return true
	})
	if err != nil {
		logging.Warn("usage recovery: replay failed", logging.F("error", err.Error()))
		return
	}
	logging.Info("usage recovery: quota windows seeded from logs",
		logging.F("since", since.Format(time.RFC3339)),
		logging.F("rows", rows),
	)
}

// Recover is the in-memory variant used by tests and tooling: fold a
// pre-loaded slice of rows into the tracker.
func (t *Tracker) Recover(rows []*model.Log) {
	for _, e := range rows {
		if e == nil {
			continue
		}
		t.Observe(e.ChannelID, e.PromptTokens, e.CompletionTokens, e.CachedTokens,
			e.ReasoningTokens, e.RealCostUSD, e.CreatedAt)
	}
}
