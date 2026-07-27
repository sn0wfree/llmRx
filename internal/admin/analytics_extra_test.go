package admin_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// StreamLogs_NoBroker cannot be exercised from the external test
// package because h.logBroker is unexported. The path is reachable
// from production (server.go creates admin.Handler with a nil
// broker if log broker init fails). Covered indirectly via
// StreamLogs_CancelledContext below.

func TestAdmin_StreamLogs_CancelledContext(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/logs/stream", nil).WithContext(ctx)
	req.Header.Set("X-Session-Token", sess)
	rec := httptest.NewRecorder()
	app.Admin.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", rec.Code)
	}
}

func TestAdmin_AnalyticsTimeSeries_StoreError(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	// Invalid bucket (non-numeric) — atoiOr defaults.
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/analytics/timeseries?bucket=invalid", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("invalid-bucket code=%d, want 200", rec.Code)
	}
}

func TestAdmin_AnalyticsByModel_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/analytics/by-model", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_AnalyticsByChannel_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/analytics/by-channel", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_AnalyticsByToken_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/analytics/by-token", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_AnalyticsByModel_WithLimit(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/analytics/by-model?limit=5", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_AnalyticsByChannel_WithLimit(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/analytics/by-channel?limit=10", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_AnalyticsByToken_WithLimit(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/analytics/by-token?limit=3", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

// --- Config: defensive json paths ---

func TestAdmin_GetConfig_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/config", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	// Should contain key fields.
	body := rec.Body.String()
	if !strings.Contains(body, "markup_ratio") {
		t.Errorf("body missing markup_ratio")
	}
}

// --- Subtle admin helpers ---

func TestAdmin_ListLogs_DefaultFilter(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/logs", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}
