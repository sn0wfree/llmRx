package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
)

// BenchmarkSQLite_Insert measures single-row INSERT latency for
// the logs table — the hot write path on the LLM gateway.
func BenchmarkSQLite_Insert(b *testing.B) {
	dir := b.TempDir()
	s, err := OpenSQLite(filepath.Join(dir, "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()

	if err := s.CreateChannel(&model.Channel{
		Name: "bench", Provider: "openai", Protocol: "openai",
		BaseURL: "https://api.example.com", Models: []string{"gpt-4"},
		Status: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		b.Fatal(err)
	}
	log := &model.Log{
		TokenID: 1, ChannelID: 1, KeyID: 1, Model: "gpt-4",
		PromptTokens: 100, CompletionTokens: 50, StatusCode: 200,
		CreatedAt: time.Now(),
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := s.CreateLog(log); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkSQLite_InsertBatch measures a 10-row batched INSERT
// inside a single transaction. The single-row path above incurs
// one fsync per insert; batching amortises that cost.
func BenchmarkSQLite_InsertBatch10(b *testing.B) {
	dir := b.TempDir()
	s, err := OpenSQLite(filepath.Join(dir, "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()

	if err := s.CreateChannel(&model.Channel{
		Name: "bench", Provider: "openai", Protocol: "openai",
		BaseURL: "https://api.example.com", Models: []string{"gpt-4"},
		Status: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		b.Fatal(err)
	}
	logs := make([]*model.Log, 10)
	for i := range logs {
		logs[i] = &model.Log{
			TokenID: 1, ChannelID: 1, KeyID: 1, Model: "gpt-4",
			PromptTokens: 100, CompletionTokens: 50, StatusCode: 200,
			CreatedAt: time.Now(),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 10 inserts inside one transaction
		tx, err := s.db.Begin()
		if err != nil {
			b.Fatal(err)
		}
		for _, l := range logs {
			if _, err := tx.Exec(
				`INSERT INTO logs(token_id, channel_id, key_id, model, prompt_tokens,
					completion_tokens, status_code, router_path, request_ip, created_at)
				 VALUES (?,?,?,?,?,?,?,?,?,?)`,
				l.TokenID, l.ChannelID, l.KeyID, l.Model, l.PromptTokens,
				l.CompletionTokens, l.StatusCode, l.RouterPath, l.RequestIP, l.CreatedAt.Unix(),
			); err != nil {
				tx.Rollback()
				b.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSQLite_Query measures a typical log-list query with
// pagination — the read path used by the admin logs page.
func BenchmarkSQLite_QueryLogs(b *testing.B) {
	dir := b.TempDir()
	s, err := OpenSQLite(filepath.Join(dir, "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()

	if err := s.CreateChannel(&model.Channel{
		Name: "bench", Provider: "openai", Protocol: "openai",
		BaseURL: "https://api.example.com", Models: []string{"gpt-4"},
		Status: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		_ = s.CreateLog(&model.Log{
			TokenID: 1, ChannelID: 1, Model: "gpt-4",
			PromptTokens: 100, CompletionTokens: 50, StatusCode: 200,
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Second),
		})
	}

	filter := LogFilter{Limit: 100, Offset: 0}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := s.QueryLogs(filter)
		if err != nil {
			b.Fatal(err)
		}
		_ = ctx
	}
}

// BenchmarkSQLite_LogStats measures the aggregate stats query —
// what powers the analytics dashboard.
func BenchmarkSQLite_LogStats(b *testing.B) {
	dir := b.TempDir()
	s, err := OpenSQLite(filepath.Join(dir, "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()

	if err := s.CreateChannel(&model.Channel{
		Name: "bench", Provider: "openai", Protocol: "openai",
		BaseURL: "https://api.example.com", Models: []string{"gpt-4"},
		Status: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		_ = s.CreateLog(&model.Log{
			TokenID: 1, ChannelID: 1, Model: "gpt-4",
			PromptTokens: 100, CompletionTokens: 50, StatusCode: 200,
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Second),
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.LogStats(); err != nil {
			b.Fatal(err)
		}
	}
}
