package clientapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/store"
)

// Enrollment is the one path that mints credentials, so its failure
// modes matter more than its happy path: a code that can be reused, or
// a secret that lands in the database in plaintext, is a real
// vulnerability rather than a bug.

func newEnrollIngestor(t *testing.T) (*Ingestor, *store.SQLite) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "edge.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// One static client keeps the control plane wired, mirroring a real
	// deployment that mixes config.yml bootstrap entries with enrollment.
	ing := NewIngestor(nil, []EdgeClient{{Name: "static", Token: "static-token"}})
	ing.SetEnrollStore(st)
	return ing, st
}

func postEnroll(t *testing.T, ing *Ingestor, token string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"enroll_token": token})
	req := httptest.NewRequest(http.MethodPost, "/client/v1/enroll", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ing.Handler().ServeHTTP(rec, req)
	return rec
}

func TestEnroll_Success(t *testing.T) {
	ing, st := newEnrollIngestor(t)

	code, expires, err := ing.IssueEnrollToken("alice-laptop")
	if err != nil {
		t.Fatalf("IssueEnrollToken: %v", err)
	}
	if !strings.HasPrefix(code, enrollTokenPrefix) {
		t.Fatalf("token %q lacks the %q prefix", code, enrollTokenPrefix)
	}
	if time.Until(expires) < 10*time.Minute {
		t.Fatalf("expiry too soon: %v", expires)
	}

	rec := postEnroll(t, ing, code)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var out enrollResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(out.ClientID, clientIDPrefix) {
		t.Fatalf("client_id = %q", out.ClientID)
	}
	if !strings.HasPrefix(out.ClientSecret, clientSecretPfx) {
		t.Fatalf("secret = %q", out.ClientSecret)
	}
	if out.Name != "alice-laptop" {
		t.Fatalf("name = %q, want the name the code was issued for", out.Name)
	}

	// The secret must work immediately, without a restart.
	if got, ok := ing.verifyClient(out.ClientSecret); !ok || got != "alice-laptop" {
		t.Fatalf("issued secret does not authenticate: %q %v", got, ok)
	}

	// And the database must hold only the hash.
	clients, err := st.ListEdgeClients()
	if err != nil {
		t.Fatalf("ListEdgeClients: %v", err)
	}
	if len(clients) != 1 {
		t.Fatalf("clients = %d", len(clients))
	}
	if clients[0].SecretHash == out.ClientSecret {
		t.Fatal("secret stored in plaintext")
	}
	if clients[0].SecretHash != hashToken(out.ClientSecret) {
		t.Fatal("stored hash is not sha256(secret)")
	}
}

// A code is single-use: the second redemption must fail.
func TestEnroll_SingleUse(t *testing.T) {
	ing, _ := newEnrollIngestor(t)
	code, _, err := ing.IssueEnrollToken("bob")
	if err != nil {
		t.Fatalf("IssueEnrollToken: %v", err)
	}

	if rec := postEnroll(t, ing, code); rec.Code != http.StatusCreated {
		t.Fatalf("first redemption: %d", rec.Code)
	}
	rec := postEnroll(t, ing, code)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("second redemption: status %d, want 401", rec.Code)
	}
}

// Concurrent redemptions of the same code must yield exactly one
// credential — the atomic claim is the whole point.
func TestEnroll_ConcurrentRedemptionYieldsOne(t *testing.T) {
	ing, st := newEnrollIngestor(t)
	code, _, err := ing.IssueEnrollToken("racer")
	if err != nil {
		t.Fatalf("IssueEnrollToken: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			codes[i] = postEnroll(t, ing, code).Code
		}(i)
	}
	wg.Wait()

	created, unauthorized := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusUnauthorized:
			unauthorized++
		}
	}
	if created != 1 || unauthorized != n-1 {
		t.Fatalf("created=%d unauthorized=%d, want exactly one success", created, unauthorized)
	}

	clients, _ := st.ListEdgeClients()
	// One enrollment plus the concurrency losers must not have created
	// extra rows.
	if len(clients) != 1 {
		t.Fatalf("clients = %d, want 1", len(clients))
	}
}

// Expiry is enforced by ConsumeEnrollToken reading expires_at against
// the caller's clock. Inject a code that is already past its window.
func TestEnroll_ExpiredCodeRejected(t *testing.T) {
	ing, st := newEnrollIngestor(t)

	// Store a code whose expiry is in the past.
	const expired = "enr_expiredcode0000000000000000"
	if err := st.CreateEnrollToken(hashToken(expired), "late",
		time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("CreateEnrollToken: %v", err)
	}

	rec := postEnroll(t, ing, expired)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for an expired code", rec.Code)
	}
}

func TestEnroll_UnknownCodeRejected(t *testing.T) {
	ing, _ := newEnrollIngestor(t)
	rec := postEnroll(t, ing, "enr_deadbeef")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	// The message must not reveal whether the code existed.
	body := rec.Body.String()
	if !strings.Contains(body, "invalid_enroll_token") {
		t.Fatalf("body = %s", body)
	}
}

// Unknown, used and expired codes must be indistinguishable: any
// difference lets a prober enumerate valid codes.
func TestEnroll_FailureModesIndistinguishable(t *testing.T) {
	ing, st := newEnrollIngestor(t)

	usedCode, _, _ := ing.IssueEnrollToken("used")
	_ = postEnroll(t, ing, usedCode)

	// A code stored with an expiry already in the past.
	const expiredCode = "enr_expired0000000000000000000000"
	if err := st.CreateEnrollToken(hashToken(expiredCode), "expired",
		time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("CreateEnrollToken: %v", err)
	}

	unknown := postEnroll(t, ing, "enr_0000000000000000")
	used := postEnroll(t, ing, usedCode)
	expired := postEnroll(t, ing, expiredCode)

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"unknown": unknown, "used": used, "expired": expired,
	} {
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status %d, want 401", name, rec.Code)
		}
	}
	if unknown.Body.String() != used.Body.String() || used.Body.String() != expired.Body.String() {
		t.Fatalf("failure bodies differ:\n unknown=%s\n used=%s\n expired=%s",
			unknown.Body.String(), used.Body.String(), expired.Body.String())
	}
}

func TestEnroll_DisabledWithoutRegistry(t *testing.T) {
	ing := NewIngestor(nil, []EdgeClient{{Name: "s", Token: "t"}})
	rec := postEnroll(t, ing, "enr_anything")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestEnroll_RequiresPOST(t *testing.T) {
	ing, _ := newEnrollIngestor(t)
	req := httptest.NewRequest(http.MethodGet, "/client/v1/enroll", nil)
	rec := httptest.NewRecorder()
	ing.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestEnroll_RequiresToken(t *testing.T) {
	ing, _ := newEnrollIngestor(t)
	rec := postEnroll(t, ing, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestIssueEnrollToken_RequiresName(t *testing.T) {
	ing, _ := newEnrollIngestor(t)
	if _, _, err := ing.IssueEnrollToken("   "); err == nil {
		t.Fatal("empty name must be rejected")
	}
}

func TestIssueEnrollToken_DisabledWithoutRegistry(t *testing.T) {
	ing := NewIngestor(nil, nil)
	if _, _, err := ing.IssueEnrollToken("x"); err == nil {
		t.Fatal("expected an error with no registry")
	}
}

// An enrolled secret signs its own config bundle, so the control plane
// keeps working after a restart without touching config.yml.
func TestEnroll_SecretWorksForControlPlane(t *testing.T) {
	ing, _ := newEnrollIngestor(t)
	code, _, _ := ing.IssueEnrollToken("edge-cp")
	rec := postEnroll(t, ing, code)
	var out enrollResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)

	// Heartbeat with the issued secret.
	req := httptest.NewRequest(http.MethodPost, "/client/v1/heartbeat",
		strings.NewReader(`{"client_version":"v1","config_version":0}`))
	req.Header.Set("Authorization", "Bearer "+out.ClientSecret)
	hb := httptest.NewRecorder()
	ing.Handler().ServeHTTP(hb, req)
	if hb.Code != http.StatusOK {
		t.Fatalf("heartbeat with enrolled secret: %d %s", hb.Code, hb.Body.String())
	}

	// Config fetch, and the bundle must verify under that secret.
	req2 := httptest.NewRequest(http.MethodGet, "/client/v1/config", nil)
	req2.Header.Set("Authorization", "Bearer "+out.ClientSecret)
	cf := httptest.NewRecorder()
	ing.Handler().ServeHTTP(cf, req2)
	if cf.Code != http.StatusOK {
		t.Fatalf("config with enrolled secret: %d", cf.Code)
	}
	var sb signedBundle
	_ = json.Unmarshal(cf.Body.Bytes(), &sb)
	if !VerifyBundle(sb.Config, sb.Signature, out.ClientSecret) {
		t.Fatal("bundle does not verify under the enrolled secret")
	}
}

// Revocation takes effect immediately: the secret stops authenticating.
func TestEnroll_RevokedSecretRejected(t *testing.T) {
	ing, st := newEnrollIngestor(t)
	code, _, _ := ing.IssueEnrollToken("revoked")
	rec := postEnroll(t, ing, code)
	var out enrollResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)

	if _, ok := ing.verifyClient(out.ClientSecret); !ok {
		t.Fatal("secret should work before revocation")
	}
	if err := st.RevokeEdgeClient(out.ClientID); err != nil {
		t.Fatalf("RevokeEdgeClient: %v", err)
	}
	if _, ok := ing.verifyClient(out.ClientSecret); ok {
		t.Fatal("revoked secret still authenticates")
	}

	// And an HTTP call is refused.
	req := httptest.NewRequest(http.MethodPost, "/client/v1/heartbeat", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+out.ClientSecret)
	hb := httptest.NewRecorder()
	ing.Handler().ServeHTTP(hb, req)
	if hb.Code != http.StatusUnauthorized {
		t.Fatalf("heartbeat after revoke: %d, want 401", hb.Code)
	}
}

// The static config list must keep working alongside enrolled clients.
func TestEnroll_StaticConfigStillWorks(t *testing.T) {
	ing, _ := newEnrollIngestor(t)
	if got, ok := ing.verifyClient("static-token"); !ok || got != "static" {
		t.Fatalf("static token stopped working: %q %v", got, ok)
	}
}

// last_seen is recorded for enrolled clients on heartbeat.
func TestEnroll_HeartbeatRecordsLastSeen(t *testing.T) {
	ing, st := newEnrollIngestor(t)
	code, _, _ := ing.IssueEnrollToken("seen")
	rec := postEnroll(t, ing, code)
	var out enrollResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)

	req := httptest.NewRequest(http.MethodPost, "/client/v1/heartbeat", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+out.ClientSecret)
	ing.Handler().ServeHTTP(httptest.NewRecorder(), req)

	clients, _ := st.ListEdgeClients()
	if len(clients) != 1 || clients[0].LastSeenAt.IsZero() {
		t.Fatalf("last_seen not recorded: %+v", clients)
	}
}

func TestEnroll_AdminWrappers(t *testing.T) {
	ing, _ := newEnrollIngestor(t)

	out, err := ing.IssueEnrollTokenJSON("admin-made")
	if err != nil {
		t.Fatalf("IssueEnrollTokenJSON: %v", err)
	}
	m := out.(map[string]any)
	if m["token"] == "" || m["expires_at"] == "" {
		t.Fatalf("admin response = %v", m)
	}
	if _, ok := m["notice"]; !ok {
		t.Fatal("response must warn the token is unrecoverable")
	}
}

func TestRevokeEdgeClientJSON_DisabledWithoutRegistry(t *testing.T) {
	ing := NewIngestor(nil, nil)
	if err := ing.RevokeEdgeClientJSON("ec_x"); err == nil {
		t.Fatal("expected an error with no registry")
	}
}
