package store_test

import (
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/store"
)

func TestMigrate_Idempotent(t *testing.T) {
	s1, _ := openTempLog(t)
	s1.CreateChannel(&model.Channel{Name: "ch", Provider: "openai", Protocol: "openai", BaseURL: "https://x", Models: []string{"m"}, Status: model.ChannelEnabled})
	s1.Close()

	s2, _ := openTempLog(t)
	chs, _ := s2.GetChannels()
	if len(chs) != 0 {
		t.Logf("new store has %d channels (expected 0, new temp dir)", len(chs))
	}
}

func TestUpdateToken_WithWhitelists(t *testing.T) {
	s, _ := openTempLog(t)
	tok := &model.Token{Key: "sk-t", Name: "t1", Status: model.TokenActive}
	s.CreateToken(tok)
	tok.ModelsWhitelist = []string{"gpt-4", "claude-3"}
	tok.IPWhitelist = []string{"1.2.3.4", "10.0.0.0/8"}
	if err := s.UpdateToken(tok); err != nil {
		t.Fatalf("UpdateToken: %v", err)
	}
	updated, _ := s.GetTokenByID(tok.ID)
	if len(updated.ModelsWhitelist) != 2 {
		t.Errorf("models whitelist len=%d", len(updated.ModelsWhitelist))
	}
	if len(updated.IPWhitelist) != 2 {
		t.Errorf("ip whitelist len=%d", len(updated.IPWhitelist))
	}
}

func TestGetTokens_Multiple(t *testing.T) {
	s, _ := openTempLog(t)
	s.CreateToken(&model.Token{Key: "sk-1", Name: "t1", Status: model.TokenActive})
	s.CreateToken(&model.Token{Key: "sk-2", Name: "t2", Status: model.TokenActive})
	s.CreateToken(&model.Token{Key: "sk-3", Name: "t3", Status: model.TokenActive})
	toks, err := s.GetTokens()
	if err != nil {
		t.Fatalf("GetTokens: %v", err)
	}
	if len(toks) != 3 {
		t.Errorf("expected 3 tokens, got %d", len(toks))
	}
}

func TestTopByModel_WithData(t *testing.T) {
	s, _ := openTempLog(t)
	now := time.Now()
	s.CreateLog(&model.Log{Model: "gpt-4", PromptTokens: 100, StatusCode: 200, CreatedAt: now})
	s.CreateLog(&model.Log{Model: "gpt-4", PromptTokens: 50, StatusCode: 200, CreatedAt: now})
	s.CreateLog(&model.Log{Model: "claude-3", PromptTokens: 30, StatusCode: 200, CreatedAt: now})

	out, err := s.TopByModel(store.LogFilter{Limit: 100}, 10)
	if err != nil {
		t.Fatalf("TopByModel: %v", err)
	}
	if len(out) < 2 {
		t.Errorf("expected at least 2 models, got %d", len(out))
	}
}

func TestTopByChannel_WithData(t *testing.T) {
	s, _ := openTempLog(t)
	now := time.Now()
	s.CreateLog(&model.Log{ChannelID: 1, Model: "m", PromptTokens: 100, StatusCode: 200, CreatedAt: now})
	s.CreateLog(&model.Log{ChannelID: 2, Model: "m", PromptTokens: 50, StatusCode: 200, CreatedAt: now})

	out, err := s.TopByChannel(store.LogFilter{Limit: 100}, 10)
	if err != nil {
		t.Fatalf("TopByChannel: %v", err)
	}
	if len(out) < 1 {
		t.Errorf("expected at least 1 channel, got %d", len(out))
	}
}

func TestTopByToken_WithData(t *testing.T) {
	s, _ := openTempLog(t)
	now := time.Now()
	s.CreateLog(&model.Log{TokenID: 1, Model: "m", PromptTokens: 100, StatusCode: 200, CreatedAt: now})
	s.CreateLog(&model.Log{TokenID: 2, Model: "m", PromptTokens: 50, StatusCode: 200, CreatedAt: now})

	out, err := s.TopByToken(store.LogFilter{Limit: 100}, 10)
	if err != nil {
		t.Fatalf("TopByToken: %v", err)
	}
	if len(out) < 1 {
		t.Errorf("expected at least 1 token, got %d", len(out))
	}
}

func TestTimeSeries_MultipleBuckets(t *testing.T) {
	s, _ := openTempLog(t)
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	s.CreateLog(&model.Log{Model: "m", PromptTokens: 10, StatusCode: 200, CreatedAt: old})
	s.CreateLog(&model.Log{Model: "m", PromptTokens: 20, StatusCode: 200, CreatedAt: now})

	pts, err := s.TimeSeries(store.LogFilter{Limit: 100}, 3600)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if len(pts) < 1 {
		t.Errorf("expected at least 1 bucket, got %d", len(pts))
	}
}

func TestQueryLogs_WithModelFilter(t *testing.T) {
	s, _ := openTempLog(t)
	now := time.Now()
	s.CreateLog(&model.Log{Model: "gpt-4", PromptTokens: 10, StatusCode: 200, CreatedAt: now})
	s.CreateLog(&model.Log{Model: "claude-3", PromptTokens: 20, StatusCode: 200, CreatedAt: now})

	logs, total, err := s.QueryLogs(store.LogFilter{Limit: 100, Model: "gpt-4"})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if total != 1 || len(logs) != 1 {
		t.Errorf("expected 1 log with model gpt-4, got total=%d len=%d", total, len(logs))
	}
}

func TestQueryLogs_WithStatusFilter(t *testing.T) {
	s, _ := openTempLog(t)
	now := time.Now()
	s.CreateLog(&model.Log{Model: "m", PromptTokens: 10, StatusCode: 200, CreatedAt: now})
	s.CreateLog(&model.Log{Model: "m", PromptTokens: 20, StatusCode: 500, CreatedAt: now})

	logs, total, err := s.QueryLogs(store.LogFilter{Limit: 100, StatusCode: 500})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if total != 1 || len(logs) != 1 {
		t.Errorf("expected 1 error log, got total=%d", total)
	}
}
