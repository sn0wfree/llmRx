package router

import (
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
)

// BenchmarkBreaker_GetEntry exercises the per-request entry lookup.
// After the sync.Map refactor this is a single LoadOrStore call
// with no locking on the hot path.
func BenchmarkBreaker_GetEntry(b *testing.B) {
	br := NewCircuitBreaker(nil)
	const channels = 32
	chIDs := make([]int64, channels)
	for i := range chIDs {
		chIDs[i] = int64(i + 1)
		br.getEntry(chIDs[i]) // warm
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = br.getEntry(chIDs[i%channels])
	}
}

// BenchmarkBreaker_RecordSuccessRecordFailure is the
// RecordSuccess + RecordFailure pair — what happens on every
// completed real request.
func BenchmarkBreaker_RecordSuccessRecordFailure(b *testing.B) {
	br := NewCircuitBreaker(nil)
	const channels = 32
	chIDs := make([]int64, channels)
	for i := range chIDs {
		chIDs[i] = int64(i + 1)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := chIDs[i%channels]
		br.RecordSuccess(id)
		br.RecordFailure(id)
	}
}

// BenchmarkBreaker_Filter is the L2 stage: filter a candidate
// list of channels to only those whose breaker is closed.
func BenchmarkBreaker_Filter(b *testing.B) {
	br := NewCircuitBreaker(nil)
	const channels = 16
	cands := make([]*model.Channel, channels)
	for i := range cands {
		cands[i] = &model.Channel{ID: int64(i + 1), Status: model.ChannelEnabled}
		br.getEntry(cands[i].ID) // warm
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = br.Filter(cands)
	}
}
