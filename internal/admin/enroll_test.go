package admin_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/clientapi"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// Enrollment endpoints are the only way to mint edge credentials, so
// the admin surface must be wired, session-guarded, and must hand the
// plaintext code back exactly once. The redemption flow itself is
// covered in internal/clientapi; these tests cover the admin wiring.

func newEnrollApp(t *testing.T) (*testhelper.App, *clientapi.Ingestor, *store.SQLite, string) {
	t.Helper()
	app := testhelper.New(t)
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "edge.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ing := clientapi.NewIngestor(nil, []clientapi.EdgeClient{{Name: "static", Token: "static-token"}})
	ing.SetEnrollStore(st)
	app.Admin.SetEdgeIngestor(ing)
	return app, ing, st, login(t, app)
}

func TestEnrollTokens_IssueReturnsCodeOnce(t *testing.T) {
	app, _, _, sess := newEnrollApp(t)

	rec := do(t, app.Admin.Routes(), http.MethodPost, "/edge-clients/enroll-tokens", sess,
		`{"name":"alice-laptop"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var issue struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
		Name      string `json:"name"`
		Notice    string `json:"notice"`
	}
	decodeJSON(t, rec, &issue)

	if !strings.HasPrefix(issue.Token, "enr_") {
		t.Fatalf("token = %q, want the enr_ prefix", issue.Token)
	}
	if issue.ExpiresAt == "" {
		t.Fatal("expires_at missing")
	}
	if issue.Name != "alice-laptop" {
		t.Fatalf("name = %q", issue.Name)
	}
	if issue.Notice == "" {
		t.Fatal("the response must warn that the code is unrecoverable")
	}

	// The code is not readable back through the listing endpoint.
	listRec := do(t, app.Admin.Routes(), http.MethodGet, "/edge-clients", sess, "")
	if strings.Contains(listRec.Body.String(), issue.Token) {
		t.Fatal("the enrollment code leaked into the client listing")
	}
}

func TestEnrollTokens_RequiresName(t *testing.T) {
	app, _, _, sess := newEnrollApp(t)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/edge-clients/enroll-tokens", sess, `{"name":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestEnrollTokens_RequiresSession(t *testing.T) {
	app, _, _, _ := newEnrollApp(t)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/edge-clients/enroll-tokens", "",
		`{"name":"x"}`)
	if rec.Code == http.StatusCreated {
		t.Fatal("unauthenticated issuance succeeded")
	}
}

// Revoking is idempotent, reported, and reflected in the listing.
func TestRevokeEdgeClient(t *testing.T) {
	app, ing, _, sess := newEnrollApp(t)

	// Create a real client through the ingestor so we have an id.
	code, _, err := ing.IssueEnrollToken("to-revoke")
	if err != nil {
		t.Fatalf("IssueEnrollToken: %v", err)
	}
	out, err := redeemCode(ing, code)
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}

	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/edge-clients/"+out.ClientID, sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	// Idempotent.
	if rec2 := do(t, app.Admin.Routes(), http.MethodDelete, "/edge-clients/"+out.ClientID, sess, ""); rec2.Code != http.StatusOK {
		t.Fatalf("second revoke status = %d", rec2.Code)
	}

	listRec := do(t, app.Admin.Routes(), http.MethodGet, "/edge-clients", sess, "")
	var list struct {
		Clients []struct {
			ClientID string `json:"client_id"`
			Revoked  bool   `json:"revoked"`
		} `json:"clients"`
	}
	decodeJSON(t, listRec, &list)
	for _, c := range list.Clients {
		if c.ClientID == out.ClientID {
			if !c.Revoked {
				t.Fatal("revoked client not flagged in the listing")
			}
			return
		}
	}
	t.Fatalf("revoked client missing from the listing: %+v", list.Clients)
}

// redeemCode drives the public enroll endpoint through the ingestor's
// handler, which is what a real client talks to.
func redeemCode(ing *clientapi.Ingestor, code string) (enrollResult, error) {
	var out enrollResult
	body, _ := json.Marshal(map[string]string{"enroll_token": code})
	req := httptest.NewRequest(http.MethodPost, "/client/v1/enroll", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ing.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		return out, fmt.Errorf("enroll: status %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		return out, err
	}
	return out, nil
}

type enrollResult struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}
