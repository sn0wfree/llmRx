package admin_test

import (
	"net/http"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func TestAdmin_UpdateChannel_FullPatch(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("ch1", "openai", "https://x", []string{"m"})
	body := `{"name":"ch2","provider":"anthropic","base_url":"https://y","protocol":"anthropic","models":["m2"],"intents":["i1"],"priority":3,"input_price":2,"output_price":4,"status":1}`
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/channels/1", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_UpdateToken_FullPatch(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddToken("sk-test", "t1")
	body := `{"name":"t2","rpm":100,"tpm":200,"status":1}`
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/tokens/1", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_CreateAlert_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	body := `{"name":"a","type":"error_rate","threshold":0.1,"window_sec":300,"cooldown_sec":600}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/alerts", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_UpdateAlert_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	do(t, app.Admin.Routes(), http.MethodPost, "/alerts", sess, `{"name":"a","type":"error_rate","threshold":0.1}`)
	body := `{"name":"a2","threshold":0.2,"enabled":false}`
	rec := do(t, app.Admin.Routes(), http.MethodPut, "/alerts/1", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_ListUsers(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/users", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_GetEffective(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/effective", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_AnalyticsByModel2(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/analytics/by-model", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_AnalyticsByChannel2(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodGet, "/analytics/by-channel", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAdmin_DeleteUser_Success2(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	hash, _ := authHashForTest("alicepw123")
	u := &model.User{Username: "alice", PasswordHash: hash, Role: model.RoleUser, Status: 1}
	app.Store.CreateUser(u)

	rec := do(t, app.Admin.Routes(), http.MethodDelete, "/users/"+itoa(u.ID), sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_ChangePassword_Success2(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	body := `{"old_password":"admin","new_password":"newpassword123"}`
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/users/1/password", sess, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdmin_ReloadAll_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	rec := do(t, app.Admin.Routes(), http.MethodPost, "/reload", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}
