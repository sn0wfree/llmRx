package logging

import (
	"io"
	"testing"
)

// BenchmarkLoggerInfoJSON exercises the JSON format hot path.
// The post-optimization version writes the pooled buffer bytes
// directly without an intermediate .String() copy.
func BenchmarkLoggerInfoJSON(b *testing.B) {
	l := New(io.Discard, LevelInfo, FormatJSON)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.Info("chat.completed",
			F("status", "ok"),
			F("model", "bench-model"),
			F("channel", "bench"),
			F("prompt", 10),
			F("completion", 200),
			F("real_usd", 0.001),
			F("duration_ms", 42),
			F("code", 200),
		)
	}
}