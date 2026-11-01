package logstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sn0wfree/llmRx/internal/model"
)

// fileMeta is the incremental rollup stored next to each JSONL file as
// <key>.meta.json. It exists so LogStats and unfiltered TopByField are
// O(files) instead of O(rows) — the two read paths that would
// otherwise scan the entire retention window on every admin page hit.
//
// Every counters struct is a flat map keyed by the group label, which
// keeps the merge step trivial (add field by field).
type fileMeta struct {
	Rows             int64                 `json:"rows"`
	PromptTokens     int64                 `json:"prompt_tokens"`
	CompletionTokens int64                 `json:"completion_tokens"`
	RealCostUSD      float64               `json:"real_cost_usd"`
	BilledCostUSD    float64               `json:"billed_cost_usd"`
	Errors           int64                 `json:"errors"`
	ByModel          map[string]*metaGroup `json:"by_model,omitempty"`
	ByChannel        map[string]*metaGroup `json:"by_channel,omitempty"`
	ByToken          map[string]*metaGroup `json:"by_token,omitempty"`
}

// metaGroup is one group's contribution to an aggregate.
type metaGroup struct {
	Count    int64   `json:"count"`
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost"`
	RealCost float64 `json:"real_cost"`
}

// add folds one log row into the rollup.
func (m *fileMeta) add(e *model.Log) {
	if m.ByModel == nil {
		m.ByModel = map[string]*metaGroup{}
		m.ByChannel = map[string]*metaGroup{}
		m.ByToken = map[string]*metaGroup{}
	}
	m.Rows++
	m.PromptTokens += int64(e.PromptTokens)
	m.CompletionTokens += int64(e.CompletionTokens)
	m.RealCostUSD += e.RealCostUSD
	m.BilledCostUSD += e.BilledCostUSD
	if e.StatusCode >= 400 {
		m.Errors++
	}
	tokens := int64(e.PromptTokens + e.CompletionTokens)
	addGroup(m.ByModel, e.Model, tokens, e.BilledCostUSD, e.RealCostUSD)
	addGroup(m.ByChannel, fieldKey("channel_id", e), tokens, e.BilledCostUSD, e.RealCostUSD)
	addGroup(m.ByToken, fieldKey("token_id", e), tokens, e.BilledCostUSD, e.RealCostUSD)
}

func addGroup(m map[string]*metaGroup, key string, tokens int64, cost, realCost float64) {
	g := m[key]
	if g == nil {
		g = &metaGroup{}
		m[key] = g
	}
	g.Count++
	g.Tokens += tokens
	g.Cost += cost
	g.RealCost += realCost
}

// fieldMap returns the rollup for a TopByField group dimension.
func (m *fileMeta) fieldMap(field string) map[string]*metaGroup {
	switch field {
	case "model":
		return m.ByModel
	case "channel_id":
		return m.ByChannel
	case "token_id":
		return m.ByToken
	}
	return nil
}

// mergeField folds src into dst in place.
func mergeField(dst map[string]*metaGroup, src map[string]*metaGroup) {
	for k, g := range src {
		d := dst[k]
		if d == nil {
			d = &metaGroup{}
			dst[k] = d
		}
		d.Count += g.Count
		d.Tokens += g.Tokens
		d.Cost += g.Cost
		d.RealCost += g.RealCost
	}
}

// metaPath returns the rollup path for a day-file key.
func metaPath(dir, key string) string {
	return filepath.Join(dir, key+metaExt)
}

// loadMeta reads the rollup for key. A missing or unparsable file
// returns nil, which callers interpret as "rebuild by scanning".
func loadMeta(dir, key string) *fileMeta {
	b, err := os.ReadFile(metaPath(dir, key))
	if err != nil {
		return nil
	}
	var m fileMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	// A rollup with no rows is indistinguishable from "unknown" and
	// would silently drop a real file's contribution, so treat it as
	// missing and rebuild.
	if m.Rows == 0 {
		return nil
	}
	return &m
}

// writeMeta persists the rollup atomically (temp + rename) so a crash
// never leaves a truncated meta that later reads as valid JSON.
func writeMeta(dir, key string, m *fileMeta) error {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("logstore: marshal meta %s: %w", key, err)
	}
	final := metaPath(dir, key)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("logstore: write meta %s: %w", key, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("logstore: rename meta %s: %w", key, err)
	}
	return nil
}

// rebuildMeta scans a whole JSONL file and reconstructs its rollup.
// Used when the .meta.json is missing (fresh upgrade from the SQLite
// backend, or a crash between the data flush and the meta write).
func rebuildMeta(path string) (*fileMeta, error) {
	m := &fileMeta{}
	_, err := scanFile(path, func(e *model.Log) bool {
		m.add(e)
		return true
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return m, nil
}
