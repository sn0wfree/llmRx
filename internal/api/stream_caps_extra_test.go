package api

import (
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/runtime"
)

// TestStreamTimeout_NilRT verifies the nil-runtime fallback in
// streamTimeout — the 5m default is hard-coded as a safety net so
// that a misconfigured admin can't accidentally disable the cap.
func TestStreamTimeout_NilRT(t *testing.T) {
	h := &Handler{rt: nil}
	if got := h.streamTimeout(); got != 5*time.Minute {
		t.Errorf("streamTimeout with nil rt = %v, want 5m", got)
	}
}

func TestStreamTimeout_Zero(t *testing.T) {
	h := &Handler{rt: runtime.New()}
	h.rt.SetStreamTimeoutSec(0)
	if got := h.streamTimeout(); got != 5*time.Minute {
		t.Errorf("streamTimeout(0) = %v, want 5m", got)
	}
}

func TestStreamTimeout_Positive(t *testing.T) {
	h := &Handler{rt: runtime.New()}
	h.rt.SetStreamTimeoutSec(120)
	if got := h.streamTimeout(); got != 2*time.Minute {
		t.Errorf("streamTimeout(120) = %v, want 2m", got)
	}
}

func TestStreamMaxBodyBytes_NilRT(t *testing.T) {
	h := &Handler{rt: nil}
	if got := h.streamMaxBodyBytes(); got != 32<<20 {
		t.Errorf("streamMaxBodyBytes with nil rt = %d, want %d", got, 32<<20)
	}
}

func TestStreamMaxBodyBytes_Zero(t *testing.T) {
	h := &Handler{rt: runtime.New()}
	h.rt.SetStreamMaxBodyBytes(0)
	if got := h.streamMaxBodyBytes(); got != 0 {
		t.Errorf("streamMaxBodyBytes(0) = %d, want 0", got)
	}
}

func TestStreamMaxBodyBytes_Positive(t *testing.T) {
	h := &Handler{rt: runtime.New()}
	h.rt.SetStreamMaxBodyBytes(4096)
	if got := h.streamMaxBodyBytes(); got != 4096 {
		t.Errorf("streamMaxBodyBytes(4096) = %d, want 4096", got)
	}
}

func TestStreamMaxBodyBytes_Large(t *testing.T) {
	h := &Handler{rt: runtime.New()}
	h.rt.SetStreamMaxBodyBytes(1 << 30) // 1GiB
	if got := h.streamMaxBodyBytes(); got != int(1<<30) {
		t.Errorf("streamMaxBodyBytes(1GiB) = %d, want %d", got, int(1<<30))
	}
}
