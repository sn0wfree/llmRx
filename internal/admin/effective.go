package admin

import (
	"encoding/json"
	"net/http"

	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/runtime"
)

// effectiveLimit caps the per-section list size. A run-away admin
// or accidental huge DB should not return a multi-megabyte JSON
// response. Anything above the cap is truncated and the section
// is flagged with an error so the operator can run the dedicated
// list endpoint for the full data.
const effectiveLimit = 1000

// effectiveSection is the standard wrapper around every list in
// the Effective response. items is the data, count is the number
// actually returned (may be < len(items) due to cap), and error
// is null on success or a human-readable string on failure.
type effectiveSection struct {
	Items any    `json:"items"`
	Count int    `json:"count"`
	Error *string `json:"error"`
}

// effectiveChannel / effectiveToken / effectivePlan / effectiveAlert
// are the summary views; deliberately narrower than the
// corresponding model types so the response stays small and
// secrets (API key ciphertext) never leak to the admin UI.
type effectiveChannel struct {
	ID         int64                 `json:"id"`
	Name       string                `json:"name"`
	Protocol   string                `json:"protocol"`
	Priority   int                   `json:"priority"`
	Status     model.ChannelStatus   `json:"status"`
	ModelCount int                   `json:"model_count"`
}

type effectiveToken struct {
	ID     int64              `json:"id"`
	Name   string             `json:"name"`
	PlanID int64              `json:"plan_id"`
	Status model.TokenStatus  `json:"status"`
	RPM    int                `json:"rpm"`
	TPM    int                `json:"tpm"`
}

type effectivePlan struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	BudgetUSD  float64 `json:"budget_usd"`
	UsedUSD    float64 `json:"used_usd"`
	MarkupRatio float64 `json:"markup_ratio"`
	Status     int     `json:"status"`
}

type effectiveAlert struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	Enabled     bool    `json:"enabled"`
	Threshold   float64 `json:"threshold"`
	CooldownSec int64   `json:"cooldown_sec"`
}

type effectiveRuntime struct {
	Values runtime.Snapshot `json:"values"`
	Source string           `json:"source"` // "db" or "yaml"
}

type effectiveResponse struct {
	Runtime    effectiveRuntime `json:"runtime"`
	YAMLSeeds  runtime.Snapshot `json:"yaml_seeds"`
	Channels   effectiveSection `json:"channels"`
	Tokens     effectiveSection `json:"tokens"`
	Plans      effectiveSection `json:"plans"`
	Alerts     effectiveSection `json:"alerts"`
}

// EffectiveConfig returns a single read-only snapshot of every
// live tunable and every CRUD entity in the gateway. It is the
// source of truth for the "Effective" tab in the admin UI.
//
// Per-section errors are isolated: a failure in one list never
// prevents the other sections from rendering. Each section also
// includes a Count field so the UI can show "Tokens (12) ▾"
// without re-counting on the client.
func (h *Handler) EffectiveConfig(w http.ResponseWriter, r *http.Request) {
	// Runtime: read the live snapshot and detect whether any DB
	// overlay exists. Source is binary at the snapshot level
	// because the entire Snapshot is persisted as a single row.
	snap := h.rt.Snapshot()
	src := "yaml"
	if raw, err := h.store.GetRuntimeSettings(); err == nil && len(raw) > 0 {
		// Validate it's still parseable; a corrupt row should
		// not flip the source label to "db" while leaving the
		// in-memory snapshot at YAML-seed values.
		var probe runtime.Snapshot
		if json.Unmarshal(raw, &probe) == nil {
			src = "db"
		}
	}

	// Build the YAML-seeds view directly from cfg so the
	// operator can compare "what was set in config.yml" vs
	// "what is in effect right now". This is intentionally not
	// a full config dump — only the fields that exist on
	// runtime.Snapshot.
	yamlSeeds := buildYAMLSeeds(h.cfg)

	resp := effectiveResponse{
		Runtime:   effectiveRuntime{Values: snap, Source: src},
		YAMLSeeds: yamlSeeds,
		Channels:  h.sectionChannels(r),
		Tokens:    h.sectionTokens(r),
		Plans:     h.sectionPlans(r),
		Alerts:    h.sectionAlerts(r),
	}

	writeJSON(w, http.StatusOK, resp)
}

func buildYAMLSeeds(cfg *config.Config) runtime.Snapshot {
	if cfg == nil {
		return runtime.Snapshot{}
	}
	return runtime.Snapshot{
		CostStrategy:       cfg.Strategy.CostStrategy,
		MarkupRatio:        cfg.Server.MarkupRatio,
		BreakerMaxFailures: int64(cfg.Server.BreakerMax),
		BreakerResetMs:     int64(cfg.Server.BreakerResetMs),
		AlertCooldownSec:   int64(cfg.Server.AlertCooldownSec),
		LogRetentionDays:   int64(cfg.Server.LogRetentionDays),
		StreamTimeoutSec:   int64(cfg.Server.StreamTimeoutSec),
		StreamMaxBodyBytes: int64(cfg.Server.StreamMaxBodyBytes),
		MaxLogSubscribers:  int64(cfg.Server.MaxLogSubscribers),
	}
}

// withSectionErr is a small helper: build the section from
// data, or set the error string. Used so each section's error
// path is one-liner.
func withSectionErr[T any](items []T, cap int, err error) effectiveSection {
	if err != nil {
		msg := err.Error()
		return effectiveSection{Items: []T{}, Count: 0, Error: &msg}
	}
	if len(items) > cap {
		truncated := items[:cap]
		msg := "truncated; use /channels (or /tokens / /plans / /alerts) for the full list"
		return effectiveSection{Items: truncated, Count: len(truncated), Error: &msg}
	}
	return effectiveSection{Items: items, Count: len(items), Error: nil}
}

// sectionChannels is a panic-isolated wrapper around
// store.GetChannels + a per-row model-count lookup.
func (h *Handler) sectionChannels(r *http.Request) (out effectiveSection) {
	defer func() {
		if rec := recover(); rec != nil {
			msg := "channels: panic during read"
			out = effectiveSection{Items: []effectiveChannel{}, Count: 0, Error: &msg}
		}
	}()
	chs, err := h.store.GetChannels()
	if err != nil {
		return withSectionErr[effectiveChannel](nil, effectiveLimit, err)
	}
	items := make([]effectiveChannel, 0, len(chs))
	for _, c := range chs {
		items = append(items, effectiveChannel{
			ID:         c.ID,
			Name:       c.Name,
			Protocol:   c.Protocol,
			Priority:   c.Priority,
			Status:     c.Status,
			ModelCount: len(c.Models),
		})
	}
	return withSectionErr(items, effectiveLimit, nil)
}

func (h *Handler) sectionTokens(r *http.Request) (out effectiveSection) {
	defer func() {
		if rec := recover(); rec != nil {
			msg := "tokens: panic during read"
			out = effectiveSection{Items: []effectiveToken{}, Count: 0, Error: &msg}
		}
	}()
	toks, err := h.store.GetTokens()
	if err != nil {
		return withSectionErr[effectiveToken](nil, effectiveLimit, err)
	}
	items := make([]effectiveToken, 0, len(toks))
	for _, t := range toks {
		items = append(items, effectiveToken{
			ID:     t.ID,
			Name:   t.Name,
			PlanID: t.PlanID,
			Status: t.Status,
			RPM:    t.RPM,
			TPM:    t.TPM,
		})
	}
	return withSectionErr(items, effectiveLimit, nil)
}

func (h *Handler) sectionPlans(r *http.Request) (out effectiveSection) {
	defer func() {
		if rec := recover(); rec != nil {
			msg := "plans: panic during read"
			out = effectiveSection{Items: []effectivePlan{}, Count: 0, Error: &msg}
		}
	}()
	ps, err := h.store.GetPlans()
	if err != nil {
		return withSectionErr[effectivePlan](nil, effectiveLimit, err)
	}
	items := make([]effectivePlan, 0, len(ps))
	for _, p := range ps {
		items = append(items, effectivePlan{
			ID:          p.ID,
			Name:        p.Name,
			BudgetUSD:   p.BudgetUSD,
			UsedUSD:     p.UsedUSD,
			MarkupRatio: p.MarkupRatio,
			Status:      p.Status,
		})
	}
	return withSectionErr(items, effectiveLimit, nil)
}

func (h *Handler) sectionAlerts(r *http.Request) (out effectiveSection) {
	defer func() {
		if rec := recover(); rec != nil {
			msg := "alerts: panic during read"
			out = effectiveSection{Items: []effectiveAlert{}, Count: 0, Error: &msg}
		}
	}()
	as, err := h.store.GetAlerts()
	if err != nil {
		return withSectionErr[effectiveAlert](nil, effectiveLimit, err)
	}
	items := make([]effectiveAlert, 0, len(as))
	for _, a := range as {
		items = append(items, effectiveAlert{
			ID:          a.ID,
			Name:        a.Name,
			Type:        string(a.Type),
			Enabled:     a.Enabled,
			Threshold:   a.Threshold,
			CooldownSec: a.CooldownSec,
		})
	}
	return withSectionErr(items, effectiveLimit, nil)
}

// _ keeps the model import live even if future refactors stop
// referencing it directly here.
var _ = model.AlertType("")
