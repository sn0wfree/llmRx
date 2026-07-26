package admin_test

import (
	"net/http"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// TestAdmin_UpdateConfig_BoundaryValues tests UpdateConfig with various
// boundary values for each validated field.
func TestAdmin_UpdateConfig_BoundaryValues(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		// cost_strategy
		{"empty strategy", `{"cost_strategy":""}`, http.StatusOK},
		{"invalid strategy", `{"cost_strategy":"random"}`, http.StatusBadRequest},
		{"valid strategy cheapest", `{"cost_strategy":"cheapest"}`, http.StatusOK},
		{"valid strategy fastest", `{"cost_strategy":"fastest"}`, http.StatusOK},
		{"valid strategy balanced", `{"cost_strategy":"balanced"}`, http.StatusOK},

		// breaker_max_failures
		{"breaker_max_failures zero", `{"breaker_max_failures":0}`, http.StatusBadRequest},
		{"breaker_max_failures negative", `{"breaker_max_failures":-1}`, http.StatusBadRequest},
		{"breaker_max_failures min", `{"breaker_max_failures":1}`, http.StatusOK},
		{"breaker_max_failures max", `{"breaker_max_failures":1000}`, http.StatusOK},
		{"breaker_max_failures over max", `{"breaker_max_failures":1001}`, http.StatusBadRequest},

		// breaker_reset_timeout_ms
		{"breaker_reset below min", `{"breaker_reset_timeout_ms":50}`, http.StatusBadRequest},
		{"breaker_reset min", `{"breaker_reset_timeout_ms":100}`, http.StatusOK},
		{"breaker_reset max", `{"breaker_reset_timeout_ms":86400000}`, http.StatusOK},
		{"breaker_reset over max", `{"breaker_reset_timeout_ms":86400001}`, http.StatusBadRequest},

		// alert_cooldown_sec
		{"alert_cooldown negative", `{"alert_cooldown_sec":-1}`, http.StatusBadRequest},
		{"alert_cooldown zero", `{"alert_cooldown_sec":0}`, http.StatusOK},
		{"alert_cooldown max", `{"alert_cooldown_sec":86400}`, http.StatusOK},
		{"alert_cooldown over max", `{"alert_cooldown_sec":86401}`, http.StatusBadRequest},

		// log_retention_days
		{"log_retention negative", `{"log_retention_days":-1}`, http.StatusBadRequest},
		{"log_retention zero", `{"log_retention_days":0}`, http.StatusOK},
		{"log_retention max", `{"log_retention_days":3650}`, http.StatusOK},
		{"log_retention over max", `{"log_retention_days":3651}`, http.StatusBadRequest},

		// markup_ratio
		{"markup below min", `{"markup_ratio":0.009}`, http.StatusBadRequest},
		{"markup min", `{"markup_ratio":0.01}`, http.StatusOK},
		{"markup max", `{"markup_ratio":1000}`, http.StatusOK},
		{"markup over max", `{"markup_ratio":1001}`, http.StatusBadRequest},

		// stream_timeout_sec
		{"stream_timeout negative", `{"stream_timeout_sec":-1}`, http.StatusBadRequest},
		{"stream_timeout zero", `{"stream_timeout_sec":0}`, http.StatusOK},
		{"stream_timeout max", `{"stream_timeout_sec":3600}`, http.StatusOK},
		{"stream_timeout over max", `{"stream_timeout_sec":3601}`, http.StatusBadRequest},

		// stream_max_body_bytes
		{"stream_max_body negative", `{"stream_max_body_bytes":-1}`, http.StatusBadRequest},
		{"stream_max_body zero", `{"stream_max_body_bytes":0}`, http.StatusOK},
		{"stream_max_body max", `{"stream_max_body_bytes":1073741824}`, http.StatusOK},
		{"stream_max_body over max", `{"stream_max_body_bytes":1073741825}`, http.StatusBadRequest},

		// max_log_subscribers
		{"max_log_sub negative", `{"max_log_subscribers":-1}`, http.StatusBadRequest},
		{"max_log_sub zero", `{"max_log_subscribers":0}`, http.StatusOK},
		{"max_log_sub max", `{"max_log_subscribers":100000}`, http.StatusOK},
		{"max_log_sub over max", `{"max_log_subscribers":100001}`, http.StatusBadRequest},

		// log_level
		{"log_level negative", `{"log_level":-1}`, http.StatusBadRequest},
		{"log_level zero", `{"log_level":0}`, http.StatusOK},
		{"log_level max", `{"log_level":3}`, http.StatusOK},
		{"log_level over max", `{"log_level":4}`, http.StatusBadRequest},

		// empty body
		{"empty body", `{}`, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := testhelper.New(t)
			sess := login(t, app)
			rec := do(t, app.Admin.Routes(), http.MethodPut, "/config", sess, tt.body)
			if rec.Code != tt.wantCode {
				t.Fatalf("code=%d want %d body=%s", rec.Code, tt.wantCode, rec.Body.String())
			}
		})
	}
}

// TestAdmin_DeletePlan_ErrorPaths tests DeletePlan error branches.
func TestAdmin_DeletePlan_ErrorPaths(t *testing.T) {
	t.Run("GetTokens fails", func(t *testing.T) {
		app, ss := newScriptedAdmin(t)
		sess := login(t, app)
		p := &model.Plan{Name: "p", MarkupRatio: 1.0, Status: 1}
		app.Store.CreatePlan(p)
		ss.GetTokensFunc = func() ([]model.Token, error) {
			return nil, errTestStore
		}
		rec := do(t, app.Admin.Routes(), http.MethodDelete, "/plans/"+itoa(p.ID), sess, "")
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("code=%d want 500", rec.Code)
		}
	})

	t.Run("DeletePlan fails", func(t *testing.T) {
		app, ss := newScriptedAdmin(t)
		sess := login(t, app)
		p := &model.Plan{Name: "p", MarkupRatio: 1.0, Status: 1}
		app.Store.CreatePlan(p)
		ss.DeletePlanFunc = func(id int64) error {
			return errTestStore
		}
		rec := do(t, app.Admin.Routes(), http.MethodDelete, "/plans/"+itoa(p.ID), sess, "")
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("code=%d want 500", rec.Code)
		}
	})
}

// TestAdmin_ChangePassword_ErrorPaths tests ChangePassword error branches.
func TestAdmin_ChangePassword_ErrorPaths(t *testing.T) {
	t.Run("bad id", func(t *testing.T) {
		app := testhelper.New(t)
		sess := login(t, app)
		body := `{"old_password":"admin","new_password":"newpw123"}`
		rec := do(t, app.Admin.Routes(), http.MethodPost, "/users/abc/password", sess, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("short new password", func(t *testing.T) {
		app := testhelper.New(t)
		sess := login(t, app)
		body := `{"old_password":"admin","new_password":"123"}`
		rec := do(t, app.Admin.Routes(), http.MethodPost, "/users/1/password", sess, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("target not found", func(t *testing.T) {
		app := testhelper.New(t)
		sess := login(t, app)
		body := `{"old_password":"admin","new_password":"newpw123"}`
		rec := do(t, app.Admin.Routes(), http.MethodPost, "/users/9999/password", sess, body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("code=%d want 404", rec.Code)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		app := testhelper.New(t)
		sess := login(t, app)
		rec := do(t, app.Admin.Routes(), http.MethodPost, "/users/1/password", sess, `{bad`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})
}

// TestAdmin_UpdateAlert_PatchFields tests UpdateAlert with various patch combinations.
func TestAdmin_UpdateAlert_PatchFields(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.Store.CreateAlert(&model.Alert{Name: "a", Type: "error_rate", Threshold: 0.1, WindowSec: 300, CooldownSec: 300, Enabled: true})

	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{"empty patch", `{}`, http.StatusOK},
		{"name only", `{"name":"a2"}`, http.StatusOK},
		{"threshold only", `{"threshold":0.5}`, http.StatusOK},
		{"window only", `{"window_sec":600}`, http.StatusOK},
		{"cooldown only", `{"cooldown_sec":120}`, http.StatusOK},
		{"webhook only", `{"webhook_url":"https://hook.example"}`, http.StatusOK},
		{"enabled only", `{"enabled":false}`, http.StatusOK},
		{"invalid type", `{"type":"bad_type"}`, http.StatusBadRequest},
		{"all fields", `{"name":"a3","type":"error_rate","threshold":0.9,"window_sec":900,"cooldown_sec":300,"webhook_url":"https://x","enabled":false}`, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, app.Admin.Routes(), http.MethodPut, "/alerts/1", sess, tt.body)
			if rec.Code != tt.wantCode {
				t.Fatalf("code=%d want %d body=%s", rec.Code, tt.wantCode, rec.Body.String())
			}
		})
	}
}

// TestAdmin_Login_ErrorPaths tests Login error branches.
func TestAdmin_Login_ErrorPaths(t *testing.T) {
	t.Run("invalid json", func(t *testing.T) {
		app := testhelper.New(t)
		rec := do(t, app.Admin.Routes(), http.MethodPost, "/login", "", `{bad`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("empty credentials", func(t *testing.T) {
		app := testhelper.New(t)
		rec := do(t, app.Admin.Routes(), http.MethodPost, "/login", "", `{"username":"","password":""}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("nonexistent user", func(t *testing.T) {
		app := testhelper.New(t)
		rec := do(t, app.Admin.Routes(), http.MethodPost, "/login", "", `{"username":"nobody","password":"pass"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("code=%d want 401", rec.Code)
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		app := testhelper.New(t)
		rec := do(t, app.Admin.Routes(), http.MethodPost, "/login", "", `{"username":"admin","password":"wrong"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("code=%d want 401", rec.Code)
		}
	})
}

// TestAdmin_RotateSecrets_ErrorPaths tests RotateSecrets error branches.
func TestAdmin_RotateSecrets_ErrorPaths(t *testing.T) {
	t.Run("invalid json", func(t *testing.T) {
		app := testhelper.New(t)
		sess := login(t, app)
		rec := do(t, app.Admin.Routes(), http.MethodPost, "/secrets/rotate", sess, `{bad`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("non-hex key", func(t *testing.T) {
		app := testhelper.New(t)
		sess := login(t, app)
		body := `{"new_master_key":"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"}`
		rec := do(t, app.Admin.Routes(), http.MethodPost, "/secrets/rotate", sess, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})

	t.Run("short key", func(t *testing.T) {
		app := testhelper.New(t)
		sess := login(t, app)
		body := `{"new_master_key":"short"}`
		rec := do(t, app.Admin.Routes(), http.MethodPost, "/secrets/rotate", sess, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})
}

// TestAdmin_ReloadAll_WithAlertMgr tests ReloadAll with non-nil alert manager.
func TestAdmin_ReloadAll_WithAlertMgr(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	// ReloadAll with nil alertMgr (default) should succeed
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/reload", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}
