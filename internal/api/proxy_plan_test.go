package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/sn0wfree/llmRx/internal/config"
	"github.com/sn0wfree/llmRx/internal/middleware"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/runtime"
	"github.com/sn0wfree/llmRx/internal/store"
)

// --- peerIsTrustedProxy branches ---

func TestPeerIsTrustedProxy_EmptyCIDRsTrustAll(t *testing.T) {
	h := &Handler{cfg: &config.Config{
		Server: config.ServerConfig{TrustedProxyCIDRs: nil},
	}}
	if !h.peerIsTrustedProxy("203.0.113.10") {
		t.Error("empty CIDR list should trust all")
	}
	if !h.peerIsTrustedProxy("10.0.0.1") {
		t.Error("empty CIDR list should trust all")
	}
}

func TestPeerIsTrustedProxy_InsideCIDR(t *testing.T) {
	h := &Handler{cfg: &config.Config{
		Server: config.ServerConfig{TrustedProxyCIDRs: []string{"10.0.0.0/8"}},
	}}
	if !h.peerIsTrustedProxy("10.0.0.1") {
		t.Error("10.0.0.1 should be inside 10.0.0.0/8")
	}
	if !h.peerIsTrustedProxy("10.255.255.254") {
		t.Error("10.255.255.254 should be inside 10.0.0.0/8")
	}
}

func TestPeerIsTrustedProxy_OutsideCIDR(t *testing.T) {
	h := &Handler{cfg: &config.Config{
		Server: config.ServerConfig{TrustedProxyCIDRs: []string{"10.0.0.0/8"}},
	}}
	if h.peerIsTrustedProxy("203.0.113.10") {
		t.Error("203.0.113.10 should NOT be inside 10.0.0.0/8")
	}
	if h.peerIsTrustedProxy("11.0.0.1") {
		t.Error("11.0.0.1 should NOT be inside 10.0.0.0/8")
	}
}

func TestPeerIsTrustedProxy_InvalidIP(t *testing.T) {
	h := &Handler{cfg: &config.Config{
		Server: config.ServerConfig{TrustedProxyCIDRs: []string{"10.0.0.0/8"}},
	}}
	if h.peerIsTrustedProxy("not-an-ip") {
		t.Error("invalid IP should not be trusted")
	}
}

func TestPeerIsTrustedProxy_InvalidCIDRSkipped(t *testing.T) {
	h := &Handler{cfg: &config.Config{
		Server: config.ServerConfig{TrustedProxyCIDRs: []string{"not-a-cidr", "10.0.0.0/8"}},
	}}
	if !h.peerIsTrustedProxy("10.0.0.5") {
		t.Error("10.0.0.5 should match the valid CIDR even with an invalid one in the list")
	}
	if h.peerIsTrustedProxy("8.8.8.8") {
		t.Error("8.8.8.8 should not match")
	}
}

func TestPeerIsTrustedProxy_IPv6(t *testing.T) {
	h := &Handler{cfg: &config.Config{
		Server: config.ServerConfig{TrustedProxyCIDRs: []string{"::1/128"}},
	}}
	if !h.peerIsTrustedProxy("::1") {
		t.Error("::1 should match ::1/128")
	}
	if h.peerIsTrustedProxy("::2") {
		t.Error("::2 should not match ::1/128")
	}
}

// --- firstTrustedProxyHeader branches ---

func TestFirstTrustedProxyHeader_NoHeaders(t *testing.T) {
	h := &Handler{cfg: &config.Config{
		Server: config.ServerConfig{TrustedProxyCIDRs: nil},
	}}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	if got := h.firstTrustedProxyHeader(r); got != "" {
		t.Errorf("no headers → empty, got %q", got)
	}
}

func TestFirstTrustedProxyHeader_XFFMultipleChains(t *testing.T) {
	h := &Handler{cfg: &config.Config{
		Server: config.ServerConfig{TrustedProxyCIDRs: nil},
	}}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.2, 10.0.0.3")
	if got := h.firstTrustedProxyHeader(r); got != "203.0.113.7" {
		t.Errorf("XFF leftmost = %q, want 203.0.113.7", got)
	}
}

func TestFirstTrustedProxyHeader_XRealIP(t *testing.T) {
	h := &Handler{cfg: &config.Config{
		Server: config.ServerConfig{TrustedProxyCIDRs: nil},
	}}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Real-IP", "203.0.113.42")
	if got := h.firstTrustedProxyHeader(r); got != "203.0.113.42" {
		t.Errorf("X-Real-IP = %q, want 203.0.113.42", got)
	}
}

func TestFirstTrustedProxyHeader_XFFPreferredOverRealIP(t *testing.T) {
	h := &Handler{cfg: &config.Config{
		Server: config.ServerConfig{TrustedProxyCIDRs: nil},
	}}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	r.Header.Set("X-Real-IP", "203.0.113.99")
	if got := h.firstTrustedProxyHeader(r); got != "203.0.113.7" {
		t.Errorf("XFF wins over X-Real-IP, got %q", got)
	}
}

func TestFirstTrustedProxyHeader_UntrustedPeer(t *testing.T) {
	h := &Handler{cfg: &config.Config{
		Server: config.ServerConfig{TrustedProxyCIDRs: []string{"10.0.0.0/8"}},
	}}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.10:1234"
	r.Header.Set("X-Forwarded-For", "10.0.0.1")
	if got := h.firstTrustedProxyHeader(r); got != "" {
		t.Errorf("untrusted peer → empty, got %q", got)
	}
}

// --- planIDFromContext branches ---

func TestPlanIDFromContext_Nil(t *testing.T) {
	if got := planIDFromContext(nil); got != 0 {
		t.Errorf("nil ctx → 0, got %d", got)
	}
}

func TestPlanIDFromContext_NoValue(t *testing.T) {
	if got := planIDFromContext(context.Background()); got != 0 {
		t.Errorf("no value → 0, got %d", got)
	}
}

func TestPlanIDFromContext_WrongType(t *testing.T) {
	ctx := context.WithValue(context.Background(), middleware.TokenInfoKey, "not-a-TokenInfo")
	if got := planIDFromContext(ctx); got != 0 {
		t.Errorf("wrong type → 0, got %d", got)
	}
}

func TestPlanIDFromContext_Valid(t *testing.T) {
	ctx := context.WithValue(context.Background(), middleware.TokenInfoKey, middleware.TokenInfo{PlanID: 42})
	if got := planIDFromContext(ctx); got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}

// --- billedCost branches ---

func TestBilledCost_NilStore(t *testing.T) {
	h := &Handler{rt: runtime.New(), store: nil}
	if got := h.billedCost(context.Background(), 1.0); got != 1.0 {
		t.Errorf("nil store → base only, got %v", got)
	}
}

func TestBilledCost_NoPlanInContext(t *testing.T) {
	s := openStore(t)
	h := &Handler{rt: runtime.New(), store: s}
	if got := h.billedCost(context.Background(), 2.0); got != 2.0 {
		t.Errorf("no plan in ctx → base only, got %v", got)
	}
}

func TestBilledCost_PlanWithZeroMarkup(t *testing.T) {
	s := openStore(t)
	p := &model.Plan{Name: "p", MarkupRatio: 0, Status: 1}
	if err := s.CreatePlan(p); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	// TokenInfo.PlanMarkupRatio is loaded by tokencache.Reload from
	// the plan; here we simulate that path with the cached value.
	ctx := context.WithValue(context.Background(), middleware.TokenInfoKey,
		middleware.TokenInfo{PlanID: p.ID, PlanMarkupRatio: p.MarkupRatio})
	h := &Handler{rt: runtime.New(), store: s}
	// MarkupRatio <= 0 → return base (not multiplied by zero)
	if got := h.billedCost(ctx, 2.0); got != 2.0 {
		t.Errorf("plan with markup=0 → base, got %v", got)
	}
}

func TestBilledCost_PlanWithValidMarkup(t *testing.T) {
	s := openStore(t)
	p := &model.Plan{Name: "p", MarkupRatio: 2.5, Status: 1}
	if err := s.CreatePlan(p); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	ctx := context.WithValue(context.Background(), middleware.TokenInfoKey,
		middleware.TokenInfo{PlanID: p.ID, PlanMarkupRatio: p.MarkupRatio})
	h := &Handler{rt: runtime.New(), store: s}
	// 1.0 * markup(1.0) * plan.MarkupRatio(2.5) = 2.5
	if got := h.billedCost(ctx, 1.0); got != 2.5 {
		t.Errorf("got %v, want 2.5", got)
	}
}

func TestBilledCost_PlanNotFound(t *testing.T) {
	s := openStore(t)
	ctx := context.WithValue(context.Background(), middleware.TokenInfoKey,
		middleware.TokenInfo{PlanID: 9999})
	h := &Handler{rt: runtime.New(), store: s}
	if got := h.billedCost(ctx, 1.0); got != 1.0 {
		t.Errorf("non-existent plan → base only, got %v", got)
	}
}

// openStore creates a temp-backed SQLite store for handler tests.
func openStore(t *testing.T) store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.OpenSQLite(dir + "/test.db")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
