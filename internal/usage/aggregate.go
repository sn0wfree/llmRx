package usage

import (
	"time"

	"github.com/sn0wfree/llmRx/internal/logstore"
	"github.com/sn0wfree/llmRx/internal/model"
)

// Aggregation helpers shared by the admin JSON API and the webui
// monitoring page. History comes from the logstore meta rollups when
// the JSONL backend is active (O(files)); other backends fall back to
// a ScanSince row stream.

// ChannelAgg accumulates one channel's usage over a query range.
type ChannelAgg struct {
	Requests         int64
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	ReasoningTokens  int64
	RealCostUSD      float64
	BilledCostUSD    float64
	Errors           int64
}

// AddRow folds one log row in.
func (a *ChannelAgg) AddRow(e *model.Log) {
	a.Requests++
	a.PromptTokens += int64(e.PromptTokens)
	a.CompletionTokens += int64(e.CompletionTokens)
	a.CachedTokens += int64(e.CachedTokens)
	a.ReasoningTokens += int64(e.ReasoningTokens)
	a.RealCostUSD += e.RealCostUSD
	a.BilledCostUSD += e.BilledCostUSD
	if e.StatusCode >= 400 {
		a.Errors++
	}
}

// Add folds another aggregate in (totals accumulation).
func (a *ChannelAgg) Add(o *ChannelAgg) {
	a.Requests += o.Requests
	a.PromptTokens += o.PromptTokens
	a.CompletionTokens += o.CompletionTokens
	a.CachedTokens += o.CachedTokens
	a.ReasoningTokens += o.ReasoningTokens
	a.RealCostUSD += o.RealCostUSD
	a.BilledCostUSD += o.BilledCostUSD
	a.Errors += o.Errors
}

// ErrorRate is the failed-request fraction (0 when no requests).
func (a *ChannelAgg) ErrorRate() float64 {
	if a.Requests == 0 {
		return 0
	}
	return float64(a.Errors) / float64(a.Requests)
}

// DayKeysSince returns the UTC "YYYY-MM-DD" keys covering
// [from-day, today], where from is day-aligned. The 62-iteration cap
// is a sanity guard against a miscomputed `from`.
func DayKeysSince(from time.Time) []string {
	today := dayStartUTC(time.Now())
	out := []string{}
	for day := dayStartUTC(from); !day.After(today) && len(out) < 62; day = day.AddDate(0, 0, 1) {
		out = append(out, day.Format("2006-01-02"))
	}
	return out
}

func dayStartUTC(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// AggregateChannelUsage computes per-channel usage since `from`.
// dayMode selects whole-day granularity (7d/30d ranges): the JSONL
// backend answers from meta rollups, other backends scan. Non-day
// mode always scans rows for a precise trailing window.
func AggregateChannelUsage(ls *logstore.Manager, from time.Time, dayMode bool) (map[int64]*ChannelAgg, error) {
	if ls == nil {
		return map[int64]*ChannelAgg{}, nil
	}
	aggs := map[int64]*ChannelAgg{}
	agg := func(id int64) *ChannelAgg {
		a := aggs[id]
		if a == nil {
			a = &ChannelAgg{}
			aggs[id] = a
		}
		return a
	}
	if dayMode {
		stats, err := ls.DailyChannelStats(DayKeysSince(from))
		if err == nil {
			for _, s := range stats {
				a := agg(s.ChannelID)
				a.Requests += s.Requests
				a.PromptTokens += s.PromptTokens
				a.CompletionTokens += s.CompletionTokens
				a.CachedTokens += s.CachedTokens
				a.ReasoningTokens += s.ReasoningTokens
				a.RealCostUSD += s.RealCostUSD
				a.BilledCostUSD += s.BilledCostUSD
				a.Errors += s.Errors
			}
			return aggs, nil
		}
		if err != logstore.ErrDailyChannelStatsUnavailable {
			return nil, err
		}
	}
	err := ls.ScanSince(from, func(e *model.Log) bool {
		if e.ChannelID > 0 {
			agg(e.ChannelID).AddRow(e)
		}
		return true
	})
	return aggs, err
}

// BucketPoint is one (bucket, channel) cell of a token trend series:
// Bucket is "2006-01-02T15Z" when hourly, "2006-01-02" otherwise.
type BucketPoint struct {
	Bucket    string
	ChannelID int64
	Prompt    int64
	Completion int64
	Requests  int64
	Cost      float64
}

// ChannelBuckets returns the per-(bucket, channel) token series.
// hourly buckets by UTC hour (row scan — the trailing 24h is small);
// otherwise buckets by day (meta rollups when available).
func ChannelBuckets(ls *logstore.Manager, from time.Time, hourly bool) ([]BucketPoint, error) {
	if ls == nil {
		return nil, nil
	}
	var out []BucketPoint
	if hourly {
		err := ls.ScanSince(from, func(e *model.Log) bool {
			if e.ChannelID <= 0 {
				return true
			}
			out = append(out, BucketPoint{
				Bucket:     e.CreatedAt.UTC().Format("2006-01-02T15Z"),
				ChannelID:  e.ChannelID,
				Prompt:     int64(e.PromptTokens),
				Completion: int64(e.CompletionTokens),
				Requests:   1,
				Cost:       e.BilledCostUSD,
			})
			return true
		})
		return out, err
	}
	stats, err := ls.DailyChannelStats(DayKeysSince(from))
	if err == logstore.ErrDailyChannelStatsUnavailable {
		err = ls.ScanSince(from, func(e *model.Log) bool {
			if e.ChannelID <= 0 {
				return true
			}
			out = append(out, BucketPoint{
				Bucket:     e.CreatedAt.UTC().Format("2006-01-02"),
				ChannelID:  e.ChannelID,
				Prompt:     int64(e.PromptTokens),
				Completion: int64(e.CompletionTokens),
				Requests:   1,
				Cost:       e.BilledCostUSD,
			})
			return true
		})
	}
	if err != nil {
		return nil, err
	}
	for _, s := range stats {
		out = append(out, BucketPoint{
			Bucket:     s.Day,
			ChannelID:  s.ChannelID,
			Prompt:     s.PromptTokens,
			Completion: s.CompletionTokens,
			Requests:   s.Requests,
			Cost:       s.BilledCostUSD,
		})
	}
	return out, nil
}

// ParseRange maps a ?range= query value to (from, name, dayMode).
func ParseRange(val string, now time.Time) (from time.Time, name string, dayMode bool) {
	switch val {
	case "7d":
		return dayStartUTC(now).AddDate(0, 0, -6), "7d", true
	case "30d":
		return dayStartUTC(now).AddDate(0, 0, -29), "30d", true
	default:
		return now.Add(-24 * time.Hour), "24h", false
	}
}
