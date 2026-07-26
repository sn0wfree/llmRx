package admin_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

var errTestStore = errors.New("test store error")

func newScriptedAdmin(t *testing.T) (*testhelper.App, *testhelper.ScriptedStore) {
	t.Helper()
	app := testhelper.New(t)
	ss := testhelper.NewScriptedStore(app.Store)
	app.Admin.SetStore(ss)
	return app, ss
}

func TestAdmin_ListChannels_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.GetChannelsFunc = func() ([]model.Channel, error) {
		return nil, errTestStore
	}
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/channels", sess, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAdmin_CreateChannel_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.CreateChannelFunc = func(ch *model.Channel) error {
		return errTestStore
	}
	body := `{"name":"ch1","provider":"openai","base_url":"https://x","models":["m"]}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels", sess, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_UpdateChannel_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.UpdateChannelFunc = func(ch *model.Channel) error {
		return errTestStore
	}
	app.AddChannel("ch1", "openai", "https://x", []string{"m"})
	body := `{"name":"ch2"}`
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/channels/1", sess, body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAdmin_DeleteChannel_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.DeleteChannelFunc = func(id int64) error {
		return errTestStore
	}
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/channels/1", sess, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAdmin_ListKeys_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.GetKeysFunc = func(channelID int64) ([]model.Key, error) {
		return nil, errTestStore
	}
	app.AddChannel("ch1", "openai", "https://x", []string{"m"})
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/channels/1/keys", sess, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAdmin_CreateKey_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.CreateKeyFunc = func(k *model.Key) error {
		return errTestStore
	}
	app.AddChannel("ch1", "openai", "https://x", []string{"m"})
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels/1/keys", sess, `{"key":"sk-test"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_DeleteKey_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.DeleteKeyFunc = func(id int64) error {
		return errTestStore
	}
	app.AddChannel("ch1", "openai", "https://x", []string{"m"}, "sk-test")
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/channels/1/keys/1", sess, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAdmin_ListTokens_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.GetTokensFunc = func() ([]model.Token, error) {
		return nil, errTestStore
	}
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/tokens", sess, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAdmin_CreateToken_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.CreateTokenFunc = func(t *model.Token) error {
		return errTestStore
	}
	body := `{"name":"t1"}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/tokens", sess, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_UpdateToken_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.UpdateTokenFunc = func(t *model.Token) error {
		return errTestStore
	}
	app.AddToken("sk-t", "t1")
	body := `{"name":"t2"}`
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/tokens/1", sess, body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAdmin_DeleteToken_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.DeleteTokenFunc = func(id int64) error {
		return errTestStore
	}
	app.AddToken("sk-t", "t1")
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/tokens/1", sess, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAdmin_ListUsers_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.GetUsersFunc = func() ([]model.User, error) {
		return nil, errTestStore
	}
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/users", sess, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAdmin_CreateUser_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.CreateUserFunc = func(u *model.User) error {
		return errTestStore
	}
	body := `{"username":"alice","password":"alicepw123","role":0}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/users", sess, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

func TestAdmin_DeleteUser_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.UpdateUserFunc = func(u *model.User) error {
		return errTestStore
	}
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/users/9999", sess, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d want 404", rec.Code)
	}
}

func TestAdmin_ListAlerts_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.GetAlertsFunc = func() ([]model.Alert, error) {
		return nil, errTestStore
	}
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/alerts", sess, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAdmin_CreateAlert_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.CreateAlertFunc = func(a *model.Alert) error {
		return errTestStore
	}
	body := `{"name":"a","type":"error_rate","threshold":0.1}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/alerts", sess, body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAdmin_UpdateAlert_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.UpdateAlertFunc = func(a *model.Alert) error {
		return errTestStore
	}
	app.Store.CreateAlert(&model.Alert{Name: "a", Type: "error_rate", Threshold: 0.1, Enabled: true})
	body := `{"name":"a2"}`
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/alerts/1", sess, body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAdmin_DeleteAlert_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.DeleteAlertFunc = func(id int64) error {
		return errTestStore
	}
	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/alerts/1", sess, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAdmin_AckAlertEvent_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.AckAlertEventFunc = func(id int64) error {
		return errTestStore
	}
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/alerts/events/1/ack", sess, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAdmin_ListAlertEvents_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.GetAlertEventsFunc = func(limit int) ([]model.AlertEvent, error) {
		return nil, errTestStore
	}
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/alerts/events?limit=10", sess, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAdmin_RotateMasterKey_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.RotateMasterKeyFunc = func(newKeyHex string) (int, error) {
		return 0, errTestStore
	}
	body := `{"new_master_key":"aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/secrets/rotate", sess, body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}

func TestAdmin_SetRuntimeSettings_StoreError(t *testing.T) {
	app, ss := newScriptedAdmin(t)
	sess := login(t, app)
	ss.SetRuntimeSettingsFunc = func(payload []byte) error {
		return errTestStore
	}
	body := `{"server":{"port":9090}}`
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/config", sess, body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", rec.Code)
	}
}
