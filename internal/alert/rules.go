package alert

import (
	"fmt"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

// Evaluate runs a single rule's check against the store. It returns
// (fired, payload, err). The payload is a small map describing the
// observed metric; it is JSON-serialised into the AlertEvent row.
//
// Thresholds are typed by AlertType:
//
//   - error_rate:     threshold is a ratio in [0,1] (0.10 = 10%).
//                     Fired when count(>=400)/count(total) >= threshold
//                     in the window and total >= 5 (avoid noise).
//   - p95_latency:    threshold is duration_ms. Approximated as the
//                     average of the slowest 5% of requests in the
//                     window (sorted via SQL "ORDER BY duration_ms DESC
//                     LIMIT N"). Fired when value >= threshold.
//   - cost_spike:     threshold is a multiplier (1.5 = 50% increase).
//                     Fired when this_window_cost >= previous_window_cost * threshold
//                     and previous_window_cost > 0.
//   - key_exhausted:  threshold is unused (0). Fired when any channel
//                     has zero active keys.
func Evaluate(r *model.Alert, now time.Time, st store.Store) (bool, map[string]any, error) {
	switch r.Type {
	case model.AlertErrorRate:
		return evalErrorRate(r, now, st)
	case model.AlertP95Latency:
		return evalP95(r, now, st)
	case model.AlertCostSpike:
		return evalCostSpike(r, now, st)
	case model.AlertKeyExhausted:
		return evalKeyExhausted(r, st)
	default:
		return false, nil, fmt.Errorf("unknown alert type: %s", r.Type)
	}
}

func evalErrorRate(r *model.Alert, now time.Time, st store.Store) (bool, map[string]any, error) {
	from := now.Add(-time.Duration(r.WindowSec) * time.Second).Unix()
	var total, errs int64
	row := st.RawQueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN status_code>=400 THEN 1 ELSE 0 END),0) FROM logs WHERE created_at >= ?`, from)
	if err := row.Scan(&total, &errs); err != nil {
		return false, nil, err
	}
	if total < 5 {
		return false, nil, nil
	}
	ratio := float64(errs) / float64(total)
	if ratio >= r.Threshold {
		return true, map[string]any{
			"window_sec":   r.WindowSec,
			"requests":     total,
			"errors":       errs,
			"error_ratio":  ratio,
			"threshold":    r.Threshold,
		}, nil
	}
	return false, nil, nil
}

func evalP95(r *model.Alert, now time.Time, st store.Store) (bool, map[string]any, error) {
	from := now.Add(-time.Duration(r.WindowSec) * time.Second).Unix()
	// Approximate p95: take the worst 5% (rounded up to at least 1) of
	// request durations in the window, average them. A more precise
	// method would use NTILE(20), but SQLite has no NTILE; this is a
	// pragmatic compromise for low-volume gateways.
	var total int64
	if err := st.RawQueryRow(`SELECT COUNT(*) FROM logs WHERE created_at >= ?`, from).Scan(&total); err != nil {
		return false, nil, err
	}
	if total < 5 {
		return false, nil, nil
	}
	n := total / 20
	if n < 1 {
		n = 1
	}
	var avgMS float64
	row := st.RawQueryRow(`SELECT COALESCE(AVG(duration_ms),0) FROM (SELECT duration_ms FROM logs WHERE created_at >= ? ORDER BY duration_ms DESC LIMIT ?)`, from, n)
	if err := row.Scan(&avgMS); err != nil {
		return false, nil, err
	}
	if avgMS >= r.Threshold {
		return true, map[string]any{
			"window_sec":   r.WindowSec,
			"requests":     total,
			"p95_ms":       avgMS,
			"threshold_ms": r.Threshold,
		}, nil
	}
	return false, nil, nil
}

func evalCostSpike(r *model.Alert, now time.Time, st store.Store) (bool, map[string]any, error) {
	w := time.Duration(r.WindowSec) * time.Second
	curFrom := now.Add(-w).Unix()
	prevFrom := now.Add(-2 * w).Unix()
	var cur, prev float64
	if err := st.RawQueryRow(`SELECT COALESCE(SUM(real_cost_usd),0) FROM logs WHERE created_at >= ?`, curFrom).Scan(&cur); err != nil {
		return false, nil, err
	}
	if err := st.RawQueryRow(`SELECT COALESCE(SUM(real_cost_usd),0) FROM logs WHERE created_at >= ? AND created_at < ?`, prevFrom, curFrom).Scan(&prev); err != nil {
		return false, nil, err
	}
	if prev <= 0 || cur <= 0 {
		return false, nil, nil
	}
	ratio := cur / prev
	if ratio >= r.Threshold {
		return true, map[string]any{
			"window_sec":         r.WindowSec,
			"current_cost_usd":   cur,
			"previous_cost_usd":  prev,
			"spike_ratio":        ratio,
			"threshold_ratio":    r.Threshold,
		}, nil
	}
	return false, nil, nil
}

func evalKeyExhausted(r *model.Alert, st store.Store) (bool, map[string]any, error) {
	rows, err := st.RawQuery(`SELECT c.id, c.name FROM channels c WHERE c.status = 1 AND NOT EXISTS (SELECT 1 FROM keys k WHERE k.channel_id = c.id AND k.status = 0)`)
	if err != nil {
		return false, nil, err
	}
	defer rows.Close()
	var drained []string
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return false, nil, err
		}
		drained = append(drained, fmt.Sprintf("%d:%s", id, name))
	}
	if len(drained) > 0 {
		return true, map[string]any{
			"drained_channels": drained,
		}, nil
	}
	return false, nil, nil
}
