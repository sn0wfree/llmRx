// Package thompson implements L5: Thompson Sampling adaptive
// channel weights for the routing pipeline.
//
// Each channel is modelled as a Beta(α, β) posterior over its
// success probability. On every route decision we sample θ_i from
// each candidate's posterior and rank by the sample. Channels that
// are succeeding will have θ samples clustered near 1, so they'll
// be selected more often; a channel that's failing will see its
// posterior shift toward 0 and its samples drift down.
//
// Priors: Beta(1, 1) (uniform) is the cold start. Successes add
// to α, failures to β. A small blend with the channel's configured
// priority prevents total starvation during exploration.
//
// Persistence: Save/Load round-trip the (alpha, beta) per channel
// so a restart doesn't drop L5 back to the uniform prior. The
// file lives next to the SQLite DB (see main.go) and is rewritten
// on graceful shutdown plus periodically via Snapshot.
package thompson

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// Sampler tracks Beta posteriors per channel ID.
type Sampler struct {
	mu                   sync.Mutex
	rng                  *rand.Rand
	state                map[int64]*beta
	blend                float64
	explore              float64
	minSamplesPerChannel int
}

// Config holds construction-time parameters.
type Config struct {
	// BlendStaticWeight is the weight (0..1) given to the channel's
	// static priority when ranking. 0.0 = pure Thompson, 1.0 =
	// ignore the posterior. Default 0.3.
	BlendStaticWeight float64

	// ExploreFraction adds U(0, fraction) noise to the final score;
	// encourages exploration even when the posterior is confident.
	// Default 0.05.
	ExploreFraction float64

	// MinSamplesPerChannel is the minimum number of (success+failure)
	// observations required before L5 overrides L3 ordering. Below
	// this threshold the L3 cost order is preserved. Default 5.
	MinSamplesPerChannel int

	// Seed for the RNG; 0 = time-based.
	Seed int64
}

// New returns a Sampler seeded from cfg.
func New(cfg Config) *Sampler {
	if cfg.BlendStaticWeight < 0 {
		cfg.BlendStaticWeight = 0
	}
	if cfg.BlendStaticWeight > 1 {
		cfg.BlendStaticWeight = 1
	}
	if cfg.ExploreFraction < 0 {
		cfg.ExploreFraction = 0
	}
	if cfg.ExploreFraction > 1 {
		cfg.ExploreFraction = 1
	}
	if cfg.Seed == 0 {
		// Time-based default so each instance gets a distinct
		// sampling sequence (the previous fixed-seed=1 made every
		// gateway draw the same numbers, which biases exploration
		// toward a fixed channel on multi-instance deploys).
		cfg.Seed = time.Now().UnixNano()
	}
	if cfg.MinSamplesPerChannel <= 0 {
		cfg.MinSamplesPerChannel = 5
	}
	return &Sampler{
		rng:                  rand.New(rand.NewSource(cfg.Seed)),
		state:                make(map[int64]*beta),
		blend:                cfg.BlendStaticWeight,
		explore:              cfg.ExploreFraction,
		minSamplesPerChannel: cfg.MinSamplesPerChannel,
	}
}

// stateFile is the on-disk schema for Save/Load. Stored as JSON
// so operators can inspect it without parsing a binary blob.
type stateFile struct {
	Version int                  `json:"version"`
	Seed    int64                `json:"seed"`
	Betas   map[int64][2]float64 `json:"betas"` // channelID -> [alpha, beta]
}

const stateVersion = 1

// Save writes the current (alpha, beta) per channel to path so a
// restart picks up where the previous process left off. The
// write is atomic: a tmp file in the same directory replaces
// the destination on success.
func (s *Sampler) Save(path string) error {
	snap := s.Snapshot()
	out := stateFile{
		Version: stateVersion,
		Seed:    0, // RNG state isn't portable; we reseed on Load.
		Betas:   snap,
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// Load reads a previously-saved state file and replaces the
// in-memory posteriors. Missing file is not an error (first run
// starts with the uniform prior). A malformed file IS an error
// — we'd rather fail to start than silently reset L5.
func (s *Sampler) Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	var sf stateFile
	if err := json.NewDecoder(f).Decode(&sf); err != nil {
		return fmt.Errorf("decode state: %w", err)
	}
	if sf.Version != stateVersion {
		return fmt.Errorf("state version %d != expected %d (delete the file to reset)", sf.Version, stateVersion)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = make(map[int64]*beta, len(sf.Betas))
	for id, ab := range sf.Betas {
		s.state[id] = &beta{alpha: ab[0], beta: ab[1]}
	}
	return nil
}

type beta struct {
	alpha float64
	beta  float64
}

func (s *Sampler) posterior(id int64) *beta {
	b, ok := s.state[id]
	if !ok {
		b = &beta{alpha: 1, beta: 1} // uniform prior
		s.state[id] = b
	}
	return b
}

// RecordSuccess updates the posterior for channel id with a success.
func (s *Sampler) RecordSuccess(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.posterior(id)
	b.alpha++
}

// RecordFailure updates the posterior for channel id with a failure.
func (s *Sampler) RecordFailure(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.posterior(id)
	b.beta++
}

// Snapshot returns the current (alpha, beta) per channel for
// inspection (tests and admin API).
func (s *Sampler) Snapshot() map[int64][2]float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int64][2]float64, len(s.state))
	for id, b := range s.state {
		out[id] = [2]float64{b.alpha, b.beta}
	}
	return out
}

// Reset clears the posterior for id back to the uniform prior.
func (s *Sampler) Reset(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.state, id)
}

// ResetAll clears every channel's posterior. Used by admin /reload.
func (s *Sampler) ResetAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = make(map[int64]*beta)
}

// Ranked is the result of a single Thompson sample: a candidate
// channel paired with the score we drew.
type Ranked struct {
	Channel *model.Channel
	Score   float64
}

// Sample draws one θ per candidate and returns them sorted by
// descending score. If any candidate has fewer than min samples
// observed, the function returns the input order unchanged (a
// no-op for the caller). This gives L3 cost routing time to
// collect baseline data before L5 starts overriding it.
func (s *Sampler) Sample(channels []*model.Channel) []Ranked {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Cold start gate: until every channel has enough samples we
	// don't perturb the order. (With a uniform prior, Beta(1,1) is
	// already "1 sample" worth of information, which is below the
	// default 5-sample gate.)
	if len(channels) > 1 {
		for _, c := range channels {
			b := s.posterior(c.ID)
			obs := b.alpha + b.beta - 2 // subtract the implicit prior "1,1"
			if obs < float64(s.minSamplesPerChannel) {
				out := make([]Ranked, len(channels))
				for i, c := range channels {
					out[i] = Ranked{Channel: c, Score: 0}
				}
				return out
			}
		}
	}
	out := make([]Ranked, 0, len(channels))
	for _, c := range channels {
		b := s.posterior(c.ID)
		theta := sampleBeta(s.rng, b.alpha, b.beta)
		static := 0.0
		if c.Priority > 0 {
			static = float64(c.Priority) / 100.0
			if static > 1 {
				static = 1
			}
		}
		score := (1-s.blend)*theta + s.blend*static
		if s.explore > 0 {
			score += s.rng.Float64() * s.explore
		}
		out = append(out, Ranked{Channel: c, Score: score})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}

// sampleBeta returns a draw from Beta(alpha, beta) using gamma
// samples. The Go stdlib (1.18) doesn't ship a Beta sampler.
func sampleBeta(rng *rand.Rand, alpha, beta float64) float64 {
	x := sampleGamma(rng, alpha, 1)
	y := sampleGamma(rng, beta, 1)
	return x / (x + y)
}

// sampleGamma returns a draw from Gamma(shape, scale). Shape must
// be > 0. Uses Marsaglia & Tsang 2000 for shape>=1, and a
// boost for shape < 1.
func sampleGamma(rng *rand.Rand, shape, scale float64) float64 {
	if shape <= 0 {
		return 0
	}
	if shape < 1 {
		return sampleGamma(rng, shape+1, scale) * math.Pow(rng.Float64(), 1/shape)
	}
	d := shape - 1.0/3.0
	c := 1.0 / math.Sqrt(9*d)
	for {
		var x, v float64
		for {
			x = rng.NormFloat64()
			v = 1 + c*x
			if v > 0 {
				break
			}
		}
		v = v * v * v
		u := rng.Float64()
		if u < 1-0.0331*(x*x)*(x*x) {
			return d * v * scale
		}
		if math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v * scale
		}
	}
}
