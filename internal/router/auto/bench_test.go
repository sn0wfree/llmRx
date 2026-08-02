package auto

import (
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/router/thompson"
)

const (
	shortText = "hi"
)

var (
	// cjkTechText is a ~2KB mixed Chinese/technical prompt: the
	// realistic worst case for the heuristic scorer (CJK rune
	// counting + regex scans over a long string).
	cjkTechText = strings.Repeat("请分析这个微服务的死锁问题，设计一个基于 goroutine 的并发方案，"+
		"比较 mutex 与 channel 两种实现的 trade-off，并给出 sql 查询优化建议。"+
		"需要考虑 oauth jwt tls 等安全细节，以及 kubernetes 部署时的 replica 和 shard 配置。", 12)

	// enCodeText is a ~2KB English code-heavy prompt.
	enCodeText = strings.Repeat("Write a function that implements a retry loop with exponential backoff, "+
		"handle deadlock avoidance using goroutines and channels, explain why the transaction "+
		"deadlock happens, compare the complexity of the two algorithms step by step, "+
		"and evaluate the edge cases for the cache invalidation. ", 12)

	// warmArmModels are candidates in cost order, used by the warm
	// pool benchmarks (arms pre-seeded past the cold-start gate).
	warmArmModels = []string{"deepseek-chat", "gpt-4o-mini", "gpt-4o", "claude-3-5-sonnet"}
)

func BenchmarkHeuristicScorer_Short(b *testing.B) {
	h := NewHeuristicScorer(DefaultThresholds())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Classify(shortText)
	}
}

func BenchmarkHeuristicScorer_CJKTech(b *testing.B) {
	h := NewHeuristicScorer(DefaultThresholds())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Classify(cjkTechText)
	}
}

func BenchmarkHeuristicScorer_EnCode(b *testing.B) {
	h := NewHeuristicScorer(DefaultThresholds())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Classify(enCodeText)
	}
}

func BenchmarkTokenEstimate_CJK(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		TokenEstimate(cjkTechText)
	}
}

// BenchmarkPoolSelect_ColdStart measures the cold-start gate path:
// no observations, so SampleArms returns the cost order untouched.
func BenchmarkPoolSelect_ColdStart(b *testing.B) {
	p := NewPool(thompson.New(thompson.Config{Seed: 1, MinSamplesPerChannel: 5}))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Select("complex", warmArmModels)
	}
}

// BenchmarkPoolSelect_Warm measures the full arm sampling + sort
// path once the gate is open (arms seeded past MinSamples).
func BenchmarkPoolSelect_Warm(b *testing.B) {
	s := thompson.New(thompson.Config{Seed: 7, MinSamplesPerChannel: 5})
	for _, m := range warmArmModels {
		for i := 0; i < 20; i++ {
			s.RecordArmSuccess(ArmKey("complex", m))
			s.RecordArmFailure(ArmKey("complex", "deepseek-chat"))
		}
	}
	p := NewPool(s)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Select("complex", warmArmModels)
	}
}

// BenchmarkSampleArms_80 scales the sampler to the full v1 arm
// space (4 tiers x ~20 models): cold-start gate over 80 keys.
func BenchmarkSampleArms_80(b *testing.B) {
	keys := make([]string, 80)
	for i := range keys {
		keys[i] = ArmKey("t0", "m"+string(rune('a'+i%26)))
	}
	s := thompson.New(thompson.Config{Seed: 3, MinSamplesPerChannel: 5})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.SampleArms(keys)
	}
}

func BenchmarkFilterContextCandidates(b *testing.B) {
	models := []string{"gpt-4o", "gpt-4o-mini", "deepseek-chat", "claude-3-5-sonnet", "gpt-3.5-turbo"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		FilterContextCandidates(models, 1200)
	}
}

// BenchmarkDecisionCore is the full in-package decision core:
// classify -> context filter -> pool select, the per-request
// overhead of the auto path before the L1-L5 route.
func BenchmarkDecisionCore(b *testing.B) {
	h := NewHeuristicScorer(DefaultThresholds())
	s := thompson.New(thompson.Config{Seed: 7, MinSamplesPerChannel: 5})
	for _, m := range warmArmModels {
		for i := 0; i < 20; i++ {
			s.RecordArmSuccess(ArmKey("complex", m))
			s.RecordArmFailure(ArmKey("complex", "deepseek-chat"))
		}
	}
	p := NewPool(s)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sc := h.Classify(enCodeText)
		cands := FilterContextCandidates(warmArmModels, TokenEstimate(enCodeText))
		p.Select(string(sc.Tier), cands)
	}
}
