package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/sn0wfree/llmRx/internal/provider"
)

var ErrTemperaturePositive = errors.New("cache: temperature > 0, non-deterministic")

type Entry struct {
	Key        string            `json:"key"`
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Body       json.RawMessage   `json:"body"`
	Usage      *provider.Usage   `json:"usage,omitempty"`
	CostUSD    float64           `json:"cost_usd"`
	ChannelID  int64             `json:"channel_id"`
	StoredAt   time.Time         `json:"stored_at"`
	HitCount   int64             `json:"hit_count"`
}

type Stats struct {
	Size    int64   `json:"size"`
	Hits    int64   `json:"hits"`
	Misses  int64   `json:"misses"`
	HitRate float64 `json:"hit_rate"`
}

type Cache interface {
	Get(ctx context.Context, key string) (*Entry, bool, error)
	Set(ctx context.Context, e *Entry, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	Purge(ctx context.Context) error
	Stats(ctx context.Context) (Stats, error)
	Close() error
}

func Key(req *provider.ChatRequest) (string, error) {
	if req.Temperature != nil && *req.Temperature > 0 {
		return "", ErrTemperaturePositive
	}
	h := sha256.New()
	h.Write([]byte(req.Model))
	h.Write([]byte{0})
	h.Write(mustJSON(canonicalMessages(req.Messages)))
	h.Write([]byte{0})
	h.Write(mustJSON(sortTools(req.Tools)))
	h.Write([]byte{0})
	if req.ResponseFormat != nil {
		h.Write(mustJSON(req.ResponseFormat))
	}
	h.Write([]byte{0})
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		h.Write([]byte{1})
	}
	h.Write([]byte{0})
	if req.Stream {
		h.Write([]byte("stream"))
	} else {
		h.Write([]byte("non-stream"))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func canonicalMessages(msgs []provider.Message) []provider.Message {
	out := make([]provider.Message, len(msgs))
	for i, m := range msgs {
		out[i] = provider.Message{
			Role:         m.Role,
			Content:      m.Content,
			Name:         m.Name,
			ToolCalls:    sortToolCalls(m.ToolCalls),
			ToolCallID:   m.ToolCallID,
			CacheControl: m.CacheControl,
			Refusal:      m.Refusal,
		}
	}
	return out
}

func sortToolCalls(tcs []provider.ToolCall) []provider.ToolCall {
	if len(tcs) < 2 {
		return tcs
	}
	out := make([]provider.ToolCall, len(tcs))
	copy(out, tcs)
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Function.Name < out[j].Function.Name
	})
	return out
}

func sortTools(tools []provider.Tool) []provider.Tool {
	if len(tools) < 2 {
		return tools
	}
	out := make([]provider.Tool, len(tools))
	copy(out, tools)
	sort.Slice(out, func(i, j int) bool {
		return out[i].Function.Name < out[j].Function.Name
	})
	return out
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("cache: json.Marshal: " + err.Error())
	}
	return b
}