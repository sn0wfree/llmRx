package webui

import (
	"context"
	"time"

	"github.com/sn0wfree/llmRx/internal/auth"
)

func contextWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 100*time.Millisecond)
}

func hashForTest(pw string) (string, error) {
	return auth.Hash(pw)
}
