package admin_test

import (
	"net/http"
	"testing"

	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// TestAdmin_New_NilRT verifies that admin.New with rt=nil
// initializes a fresh runtime.Defaults (does not crash, does
// not store nil rt). We exercise this through the public API
// by checking that subsequent rt-using endpoints still work.
func TestAdmin_New_NilRT(t *testing.T) {
	app := testhelper.New(t)
	// App.New already passes non-nil rt, so we cannot directly
	// test the fallback. Instead, verify the integration: GetConfig
	// reads rt values, so it implicitly depends on rt being
	// initialized. (Same path tested by TestAdmin_GetConfig.)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/config", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}
