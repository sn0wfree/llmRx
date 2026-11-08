package logstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/sn0wfree/llmRx/internal/model"
)

// metaVersion is the schema version stamped into every .meta.json.
// v1 (pre-P21) carried only combined token totals; v2 adds the
// prompt/completion/cached/reasoning split and per-group error
// counts. loadMeta rejects older versions so they rebuild from the
// data file on first use.
const metaVersion = 2

// fileMeta is the incremental rollup stored next to each JSONL file as
// <key>.meta.json. It exists so LogStats and unfiltered TopByField are
// O(files) instead of O(rows) — the two read paths that would
// otherwise scan the entire retention window on every admin page hit.
//
// Every counters struct is a flat map keyed by the group label, which
// keeps the merge step trivial (add field by field).
type fileMeta struct {
	MetaVersion      int                   `json:"meta_version"`
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

// newFileMeta returns a versioned empty rollup.
func newFileMeta() *fileMeta { return &fileMeta{MetaVersion: metaVersion} }

// metaGroup is one group's contribution to an aggregate.
type metaGroup struct {
	Count    int64   `json:"count"`
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost"`
	RealCost float64 `json:"real_cost"`
	// v2 fields (P21): token split and per-group errors so the
	// monitoring page can render per-channel tables and daily series
	// without scanning rows.
	Prompt     int64 `json:"prompt,omitempty"`
	Completion int64 `json:"completion,omitempty"`
	Cached     int64 `json:"cached,omitempty"`
	Reasoning  int64 `json:"reasoning,omitempty"`
	Errors     int64 `json:"errors,omitempty"`
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
	addGroup(m.ByModel, e.Model, e)
	addGroup(m.ByChannel, fieldKey("channel_id", e), e)
	addGroup(m.ByToken, fieldKey("token_id", e), e)
}

func addGroup(m map[string]*metaGroup, key string, e *model.Log) {
	g := m[key]
	if g == nil {
		g = &metaGroup{}
		m[key] = g
	}
	g.Count++
	g.Tokens += int64(e.PromptTokens + e.CompletionTokens)
	g.Cost += e.BilledCostUSD
	g.RealCost += e.RealCostUSD
	g.Prompt += int64(e.PromptTokens)
	g.Completion += int64(e.CompletionTokens)
	g.Cached += int64(e.CachedTokens)
	g.Reasoning += int64(e.ReasoningTokens)
	if e.StatusCode >= 400 {
		g.Errors++
	}
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
		d.Prompt += g.Prompt
		d.Completion += g.Completion
		d.Cached += g.Cached
		d.Reasoning += g.Reasoning
		d.Errors += g.Errors
	}
}

// metaPath returns the rollup path for a day-file key.
func metaPath(dir, key string) string {
	return filepath.Join(dir, key+metaExt)
}

// loadMeta reads the rollup for key. A missing, unparsable, or
// out-of-date file returns nil, which callers interpret as "rebuild by
// scanning".
func loadMeta(dir, key string) *fileMeta {
	b, err := os.ReadFile(metaPath(dir, key))
	if err != nil {
		return nil
	}
	var m fileMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	// Pre-P21 rollups lack the token split the monitoring page reads;
	// treat them as missing so they rebuild once with v2 fields.
	if m.MetaVersion < metaVersion {
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
	m := newFileMeta()
	_, err := scanFile(path, func(e *model.Log) bool {
		m.add(e)
		return true
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return m, nil
}

// dailyChannelStats answers Manager.DailyChannelStats from the meta
// rollups: one row per (day file key, channel group). Same-day seq
// files (the "-N" rollovers) each contribute their own rows — callers
// aggregating per day should group by the Day field, which carries the
// full key.
func (d *JSONLDriver) dailyChannelStats(days []string) ([]DailyChannelStat, error) {
	d.flushActive()
	out := []DailyChannelStat{}
	for _, key := range d.resolveDays(days) {
		meta, err := d.metaFor(key)
		if err != nil {
			return nil, err
		}
		for chKey, g := range meta.ByChannel {
			id, perr := strconv.ParseInt(chKey, 10, 64)
			if perr != nil || id <= 0 {
				continue
			}
			out = append(out, DailyChannelStat{
				Day:              key,
				ChannelID:        id,
				Requests:         g.Count,
				PromptTokens:     g.Prompt,
				CompletionTokens: g.Completion,
				CachedTokens:     g.Cached,
				ReasoningTokens:  g.Reasoning,
				RealCostUSD:      g.RealCost,
				BilledCostUSD:    g.Cost,
				Errors:           g.Errors,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Day != out[j].Day {
			return out[i].Day < out[j].Day
		}
		return out[i].ChannelID < out[j].ChannelID
	})
	return out, nil
}
