package admin_test

import (
	"net/http"
	"testing"

	"github.com/sn0wfree/llmRx/internal/clientapi"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// The admin control-plane endpoints are thin adapters over clientapi;
// these tests assert the wiring (routes, status codes, version bump)
// rather than re-testing the control plane itself.

func TestEdgeClients_NotConfigured(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	rec := do(t, app.Admin.Routes(), http.MethodGet, "/edge-clients", sess, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when no edge clients are wired", rec.Code)
	}
}

func TestEdgeClients_ListsAndPushes(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	ing := clientapi.NewIngestor(nil, []clientapi.EdgeClient{
		{Name: "edge-1", Token: "tok-1"},
	})
	app.Admin.SetEdgeIngestor(ing)

	// Read the initial state.
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/edge-clients", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var listResp struct {
		Clients []map[string]any `json:"clients"`
		Config  map[string]any   `json:"config"`
	}
	decodeJSON(t, rec, &listResp)
	if len(listResp.Clients) != 1 {
		t.Fatalf("clients = %v", listResp.Clients)
	}
	if listResp.Clients[0]["name"] != "edge-1" {
		t.Fatalf("client = %v", listResp.Clients[0])
	}
	if listResp.Config["version"] != float64(1) {
		t.Fatalf("initial config version = %v, want 1", listResp.Config["version"])
	}

	// Push a new bundle.
	body := `{"cache":{"enabled":false,"ttl_sec":60,"max_items":10},
	          "fallback":{"mode":"local"},"limits":{"max_inflight":8},
	          "servers":["https://edge-a.example"]}`
	rec2 := do(t, app.Admin.Routes(), http.MethodPut, "/edge-clients/config", sess, body)
	if rec2.Code != http.StatusOK {
		t.Fatalf("push status = %d body = %s", rec2.Code, rec2.Body.String())
	}
	var pushResp struct {
		ConfigVersion int `json:"config_version"`
	}
	decodeJSON(t, rec2, &pushResp)
	if pushResp.ConfigVersion != 2 {
		t.Fatalf("pushed version = %d, want 2", pushResp.ConfigVersion)
	}

	// And it is readable back.
	rec3 := do(t, app.Admin.Routes(), http.MethodGet, "/edge-clients", sess, "")
	var after struct {
		Config struct {
			Version  int `json:"version"`
			Servers  []string `json:"servers"`
			Cache    struct {
				Enabled bool `json:"enabled"`
				TTLSec  int  `json:"ttl_sec"`
			} `json:"cache"`
			Fallback struct {
				Mode string `json:"mode"`
			} `json:"fallback"`
		} `json:"config"`
	}
	decodeJSON(t, rec3, &after)
	if after.Config.Version != 2 {
		t.Fatalf("version = %d, want 2", after.Config.Version)
	}
	if len(after.Config.Servers) != 1 || after.Config.Servers[0] != "https://edge-a.example" {
		t.Fatalf("servers = %v", after.Config.Servers)
	}
	if after.Config.Cache.Enabled {
		t.Fatal("cache.enabled should be false after the push")
	}
	if after.Config.Fallback.Mode != "local" {
		t.Fatalf("fallback = %q", after.Config.Fallback.Mode)
	}
}

// A client-supplied version must be ignored: the server owns versioning.
func TestEdgeClients_PushIgnoresClientVersion(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	ing := clientapi.NewIngestor(nil, []clientapi.EdgeClient{{Name: "e", Token: "t"}})
	app.Admin.SetEdgeIngestor(ing)

	rec := do(t, app.Admin.Routes(), http.MethodPut, "/edge-clients/config", sess,
		`{"version":999,"cache":{"enabled":true}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ConfigVersion int `json:"config_version"`
	}
	decodeJSON(t, rec, &resp)
	if resp.ConfigVersion != 2 {
		t.Fatalf("version = %d, want the server-assigned 2", resp.ConfigVersion)
	}
}

func TestEdgeClients_PushRejectsInvalidJSON(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	ing := clientapi.NewIngestor(nil, []clientapi.EdgeClient{{Name: "e", Token: "t"}})
	app.Admin.SetEdgeIngestor(ing)

	rec := do(t, app.Admin.Routes(), http.MethodPut, "/edge-clients/config", sess, `{nope`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// Both endpoints are session-guarded like the rest of the admin API.
func TestEdgeClients_RequiresSession(t *testing.T) {
	app := testhelper.New(t)
	ing := clientapi.NewIngestor(nil, []clientapi.EdgeClient{{Name: "e", Token: "t"}})
	app.Admin.SetEdgeIngestor(ing)

	if rec := do(t, app.Admin.Routes(), http.MethodGet, "/edge-clients", "", ""); rec.Code == http.StatusOK {
		t.Fatal("unauthenticated GET succeeded")
	}
	if rec := do(t, app.Admin.Routes(), http.MethodPut, "/edge-clients/config", "", `{}`); rec.Code == http.StatusOK {
		t.Fatal("unauthenticated PUT succeeded")
	}
}
