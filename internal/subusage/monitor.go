package subusage

import (
	"context"
	"encoding/json"
	"math/rand"
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

// Config controls the probe loop. Mirrors the P14 balance monitor so
// operators have one mental model for both.
type Config struct {
	// Interval between sweeps; 0 disables the periodic loop (manual
	// refresh still works).
	Interval time.Duration
	// Timeout bounds a single vendor call.
	Timeout time.Duration
	// Jitter is the fraction of Interval used to spread replicas.
	Jitter float64
}

// DefaultConfig mirrors balance.DefaultConfig.
var DefaultConfig = Config{
	Interval: time.Hour,
	Timeout:  15 * time.Second,
	Jitter:   0.1,
}

// historyDedupWindow is the minimum gap between two history rows for
// the same channel. A sweep that sees the same numbers as the last
// one is not worth a row; without this the table grows with the sweep
// count, not with real change.
const historyDedupWindow = 10 * time.Minute

// historyRetention is how long usage points are kept.
const historyRetention = 90 * 24 * time.Hour

// ChannelLister supplies the channels to probe.
type ChannelLister interface {
	GetAllChannels() []*model.Channel
}

// KeyProvider supplies a decrypted upstream key for a channel. The
// openrouter parser needs one; cookie scrapers ignore it.
type KeyProvider interface {
	NextKey(channelID int64) (*model.Key, error)
}

// StateStore persists the latest report and the history.
type StateStore interface {
	UpdateChannelUsageState(channelID int64, state string, checkedAt time.Time, probeErr string) error
	AppendUsagePoint(p store.UsagePoint) error
	UsagePoints(channelID int64, from time.Time, limit int) ([]store.UsagePoint, error)
	PruneUsagePoints(cutoff time.Time) (int64, error)
}

// SecretDecrypter decrypts the channel's stored cookie. Implemented by
// *secrets.Manager via an adapter, or nil when plaintext dev mode is
// in use.
type SecretDecrypter interface {
	Decrypt(string) ([]byte, error)
}

// Sealer encrypts a cookie for storage. Same interface, used by the
// admin write path.
type Sealer interface {
	Encrypt([]byte) (string, error)
}

// Monitor polls every eligible channel's vendor usage on a timer.
type Monitor struct {
	cfg      Config
	chans    ChannelLister
	keys     KeyProvider
	store    StateStore
	dec      SecretDecrypter
	probe    func(ctx context.Context, ch *model.Channel, in Input) (Report, error)
	retain   func() time.Duration
	mu       sync.Mutex
	lastSweep time.Time

	stopOnce sync.Once
	stop     chan struct{}
	wg       sync.WaitGroup
}

// New builds a monitor. Zero Interval falls back to DefaultConfig.
func New(cfg Config, chans ChannelLister, keys KeyProvider, st StateStore) *Monitor {
	if cfg.Interval == 0 && cfg.Timeout == 0 {
		cfg = DefaultConfig
	}
	if cfg.Interval <= 0 && cfg.Timeout <= 0 {
		cfg.Interval = DefaultConfig.Interval
		cfg.Timeout = DefaultConfig.Timeout
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultConfig.Timeout
	}
	if cfg.Jitter <= 0 {
		cfg.Jitter = DefaultConfig.Jitter
	}
	m := &Monitor{
		cfg:   cfg,
		chans: chans,
		keys:  keys,
		store: st,
		stop:  make(chan struct{}),
	}
	m.probe = func(ctx context.Context, ch *model.Channel, in Input) (Report, error) {
		return Probe(ctx, ch.UsageProbe.Kind, in)
	}
	return m
}

// SetDecrypter wires the secrets manager used to unseal cookies.
func (m *Monitor) SetDecrypter(d SecretDecrypter) { m.dec = d }

// SetProbe replaces the probe function (tests).
func (m *Monitor) SetProbe(f func(ctx context.Context, ch *model.Channel, in Input) (Report, error)) {
	if f != nil {
		m.probe = f
	}
}

// SetRetention overrides the history window (read live per sweep).
func (m *Monitor) SetRetention(f func() time.Duration) {
	if f != nil {
		m.retain = f
	}
}

// Start runs the sweep loop until ctx is cancelled or Stop is called.
func (m *Monitor) Start(ctx context.Context) {
	if m.cfg.Interval <= 0 {
		return
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		timer := time.NewTimer(m.jittered(m.cfg.Interval))
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stop:
				return
			case <-timer.C:
				m.Sweep(ctx)
				timer.Reset(m.jittered(m.cfg.Interval))
			}
		}
	}()
}

// Stop terminates the loop and waits for the in-flight sweep.
func (m *Monitor) Stop() {
	m.stopOnce.Do(func() { close(m.stop) })
	m.wg.Wait()
}

func (m *Monitor) jittered(base time.Duration) time.Duration {
	if m.cfg.Jitter <= 0 {
		return base
	}
	span := int64(float64(base) * m.cfg.Jitter)
	if span <= 0 {
		return base
	}
	// #nosec G404 -- jitter only needs spread, not unpredictability.
	return base + time.Duration(rand.Int63n(2*span)-span)
}

// eligible reports whether a channel should be probed. Disabled
// channels and channels without a probe kind are skipped.
func (m *Monitor) eligible(ch *model.Channel) bool {
	return ch != nil && ch.Status == model.ChannelEnabled && ch.UsageProbe.Enabled()
}

// Sweep probes every eligible channel and writes the results back.
func (m *Monitor) Sweep(ctx context.Context) {
	if m.chans == nil || m.store == nil {
		return
	}
	start := time.Now()

	m.mu.Lock()
	m.lastSweep = start
	m.mu.Unlock()

	chs := m.chans.GetAllChannels()
	probed, failed, skipped := 0, 0, 0
	for _, ch := range chs {
		if !m.eligible(ch) {
			skipped++
			continue
		}
		// Honour a per-channel interval override: skip when the last
		// probe is younger than the channel's own cadence.
		if ch.UsageProbe.IntervalSec > 0 && ch.UsageCheckedAt != nil {
			if start.Sub(*ch.UsageCheckedAt) < time.Duration(ch.UsageProbe.IntervalSec)*time.Second {
				skipped++
				continue
			}
		}
		if _, err := m.QueryChannel(ctx, ch); err != nil {
			failed++
			continue
		}
		probed++
	}

	if year := m.historyRetention(); year > 0 {
		if n, err := m.store.PruneUsagePoints(start.Add(-year)); err != nil {
			logging.Warn("subusage: prune failed", logging.F("error", err.Error()))
		} else if n > 0 {
			logging.Debug("subusage: pruned history", logging.F("rows", n))
		}
	}

	logging.Info("subusage: sweep done",
		logging.F("probed", probed),
		logging.F("failed", failed),
		logging.F("skipped", skipped),
		logging.F("duration_ms", time.Since(start).Milliseconds()),
	)
}

func (m *Monitor) historyRetention() time.Duration {
	if m.retain != nil {
		return m.retain()
	}
	return historyRetention
}

// QueryChannel probes one channel and persists the outcome. It is the
// shared path for the sweep and the manual-refresh endpoint, so both
// leave identical state behind.
func (m *Monitor) QueryChannel(ctx context.Context, ch *model.Channel) (Report, error) {
	now := time.Now().UTC()

	in, ierr := m.buildInput(ch)
	if ierr != nil {
		// Configuration problem: record it, don't retry upstream.
		_ = m.store.UpdateChannelUsageState(ch.ID, "", now, ierr.Error())
		return Report{}, ierr
	}

	qctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()

	rep, err := m.probe(qctx, ch, in)
	if err != nil {
		_ = m.store.UpdateChannelUsageState(ch.ID, "", now, err.Error())
		return rep, err
	}

	// Persist the good report.
	rep.channelID = ch.ID
	payload, jerr := json.Marshal(reportForStorage(rep))
	if jerr != nil {
		_ = m.store.UpdateChannelUsageState(ch.ID, "", now, "marshal report: "+jerr.Error())
		return rep, jerr
	}
	if err := m.store.UpdateChannelUsageState(ch.ID, string(payload), now, ""); err != nil {
		return rep, err
	}

	m.recordHistory(ch, rep, now)
	return rep, nil
}

// buildInput assembles the parser input from channel config + keys.
func (m *Monitor) buildInput(ch *model.Channel) (Input, error) {
	in := Input{
		BaseURL:     ch.BaseURL,
		EndpointURL: ch.UsageProbe.EndpointURL,
		JSONPath:    ch.UsageProbe.JSONPath,
		WindowName:  ch.UsageProbe.WindowName,
		Unit:        ch.UsageProbe.Unit,
	}
	if ch.UsageProbe.CookieCipher != "" && m.dec != nil {
		plain, err := m.dec.Decrypt(ch.UsageProbe.CookieCipher)
		if err != nil {
			return in, err
		}
		in.Cookie = string(plain)
	}
	// Parsers that authenticate with the channel key need one; fetch it
	// lazily so cookie-only probes do not touch the key pool.
	switch ch.UsageProbe.Kind {
	case model.UsageProbeOpenRouter, model.UsageProbeCustom, model.UsageProbeZhipuWeb:
		if m.keys != nil {
			if k, err := m.keys.NextKey(ch.ID); err == nil && k != nil {
				in.APIKey = k.Key
			}
		}
	}
	return in, nil
}

// recordHistory appends one row per window, skipping identical
// observations inside the dedup window.
func (m *Monitor) recordHistory(ch *model.Channel, rep Report, now time.Time) {
	if len(rep.Points) == 0 {
		return
	}
	var last []store.UsagePoint
	if prev, err := m.store.UsagePoints(ch.ID, now.Add(-historyDedupWindow), 200); err == nil {
		last = prev
	}
	for _, p := range rep.Points {
		if unchangedSince(last, p, now) {
			continue
		}
		row := store.UsagePoint{
			ChannelID: ch.ID,
			TS:        now,
			Source:    rep.Source,
			Window:    p.Window,
			Used:      p.Used,
			LimitVal:  p.Limit,
			Remaining: p.Remaining,
			Unit:      p.Unit,
			ResetsAt:  p.ResetsAt,
		}
		if err := m.store.AppendUsagePoint(row); err != nil {
			logging.Warn("subusage: append history failed", logging.F("error", err.Error()))
		}
	}
}

// unchangedSince reports whether the same window had an identical
// reading within the dedup window.
func unchangedSince(prev []store.UsagePoint, p UsagePoint, now time.Time) bool {
	for _, q := range prev {
		if q.Window != p.Window {
			continue
		}
		if q.Used == p.Used && q.Remaining == p.Remaining && now.Sub(q.TS) < historyDedupWindow {
			return true
		}
	}
	return false
}

// reportForStorage strips fields that must not round-trip through the
// DB (the unexported channel id is already excluded by json tags).
func reportForStorage(r Report) Report {
	r.channelID = 0
	return r
}

// ReportFromState decodes a persisted report. A malformed state is
// returned as an empty report rather than an error: a corrupt cache
// must never break the subscription page.
func ReportFromState(state string) Report {
	if state == "" {
		return Report{}
	}
	var r Report
	if err := json.Unmarshal([]byte(state), &r); err != nil {
		return Report{}
	}
	return r
}
