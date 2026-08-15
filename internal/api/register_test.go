package api_test

import (
	"time"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

func seedInviteCode(t *testing.T, app *testhelper.App, code string, quota float64) {
	t.Helper()
	if err := app.Store.CreateRedeemCode(&model.RedeemCode{
		Code: code, Kind: model.RedeemKindInvite,
		InitialQuota: quota, Role: model.RoleUser, CreatedBy: 1,
	}); err != nil {
		t.Fatalf("seed invite: %v", err)
	}
}

func TestRegisterFlow(t *testing.T) {
	app := testhelper.New(t)
	seedInviteCode(t, app, "RXM-TEST0001", 10)

	body := `{"username":"alice","password":"password123","invite_code":"RXM-TEST0001"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(body))
	req.RemoteAddr = "1.2.3.4:9999"
	rec := httptest.NewRecorder()
	app.Chat.Register(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["api_key"] == nil || out["budget_usd"] != 10.0 {
		t.Errorf("register response: %+v", out)
	}
	// Auto-login cookie set.
	cookies := rec.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == "llmrx_session" && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Error("expected llmrx_session cookie for auto-login")
	}

	// User, token, plan actually persisted.
	u, err := app.Store.GetUserByUsername("alice")
	if err != nil || u == nil {
		t.Fatalf("user not created: %v", err)
	}
	tokens, _ := app.Store.GetTokens()
	var tok *model.Token
	for i := range tokens {
		if tokens[i].Name == "alice" {
			tok = &tokens[i]
		}
	}
	if tok == nil {
		t.Fatal("token not created for alice")
	}
	plan, err := app.Store.GetPlan(tok.PlanID)
	if err != nil || plan.BudgetUSD != 10 {
		t.Fatalf("plan: %+v err=%v", plan, err)
	}
}

func TestRegisterRejectsReuseAndTaken(t *testing.T) {
	app := testhelper.New(t)
	seedInviteCode(t, app, "RXM-TEST0002", 5)

	body := `{"username":"bob","password":"password123","invite_code":"RXM-TEST0002"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(body))
	req.RemoteAddr = "1.2.3.4:9999"
	rec := httptest.NewRecorder()
	app.Chat.Register(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first register: %d", rec.Code)
	}

	// Reuse same code → 400.
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(body))
	req2.RemoteAddr = "1.2.3.4:9999"
	rec2 := httptest.NewRecorder()
	app.Chat.Register(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("reuse code: want 400, got %d", rec2.Code)
	}

	// Same username, new code → 409.
	seedInviteCode(t, app, "RXM-TEST0003", 5)
	body3 := `{"username":"bob","password":"password123","invite_code":"RXM-TEST0003"}`
	req3 := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(body3))
	req3.RemoteAddr = "1.2.3.4:9999"
	rec3 := httptest.NewRecorder()
	app.Chat.Register(rec3, req3)
	if rec3.Code != http.StatusConflict {
		t.Errorf("taken username: want 409, got %d", rec3.Code)
	}
}

func TestRegisterRateLimit(t *testing.T) {
	app := testhelper.New(t)
	app.Chat.SetRegisterLimit(2, time.Hour) // 2/hour

	seed := func(code string) {
		app.Store.CreateRedeemCode(&model.RedeemCode{
			Code: code, Kind: model.RedeemKindInvite,
			InitialQuota: 5, Role: model.RoleUser, CreatedBy: 1,
		})
	}
	seed("RXM-RL000001")
	seed("RXM-RL000002")
	seed("RXM-RL000003")

	for i, code := range []string{"RXM-RL000001", "RXM-RL000002", "RXM-RL000003"} {
		body := `{"username":"rl` + string(rune('a'+i)) + `","password":"password123","invite_code":"` + code + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(body))
		req.RemoteAddr = "9.9.9.9:1"
		rec := httptest.NewRecorder()
		app.Chat.Register(rec, req)
		if i < 2 && rec.Code != http.StatusOK {
			t.Fatalf("register %d: want 200, got %d", i, rec.Code)
		}
		if i == 2 && rec.Code != http.StatusTooManyRequests {
			t.Errorf("register %d: want 429, got %d", i, rec.Code)
		}
	}
}

func TestMeAndRedeem(t *testing.T) {
	app := testhelper.New(t)
	seedInviteCode(t, app, "RXM-TEST0004", 5)

	// Register alice.
	body := `{"username":"carol","password":"password123","invite_code":"RXM-TEST0004"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(body))
	req.RemoteAddr = "1.2.3.4:9999"
	rec := httptest.NewRecorder()
	app.Chat.Register(rec, req)
	cookie := ""
	for _, c := range rec.Result().Cookies() {
		if c.Name == "llmrx_session" {
			cookie = c.Value
		}
	}
	if cookie == "" {
		t.Fatal("no session cookie")
	}

	// /api/v1/me with cookie.
	meReq := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	meReq.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	meRec := httptest.NewRecorder()
	app.Chat.Me(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("me: %d %s", meRec.Code, meRec.Body.String())
	}
	var me map[string]any
	if err := json.Unmarshal(meRec.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me["user"] == nil || me["budget_usd"] != 5.0 {
		t.Errorf("me: %+v", me)
	}

	// Redeem a top-up code.
	if err := app.Store.CreateRedeemCode(&model.RedeemCode{
		Code: "RXM-TOPUP01", Kind: model.RedeemKindTopup,
		AmountUSD: 20, CreatedBy: 1,
	}); err != nil {
		t.Fatal(err)
	}
	rdReq := httptest.NewRequest(http.MethodPost, "/api/v1/me/redeem",
		strings.NewReader(`{"code":"RXM-TOPUP01"}`))
	rdReq.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rdRec := httptest.NewRecorder()
	app.Chat.Redeem(rdRec, rdReq)
	if rdRec.Code != http.StatusOK {
		t.Fatalf("redeem: %d %s", rdRec.Code, rdRec.Body.String())
	}
	var rd map[string]any
	json.Unmarshal(rdRec.Body.Bytes(), &rd)
	if rd["budget_usd"] != 25.0 {
		t.Errorf("redeem budget: %+v", rd)
	}

	// Reuse top-up → 400.
	rdReq2 := httptest.NewRequest(http.MethodPost, "/api/v1/me/redeem",
		strings.NewReader(`{"code":"RXM-TOPUP01"}`))
	rdReq2.AddCookie(&http.Cookie{Name: "llmrx_session", Value: cookie})
	rdRec2 := httptest.NewRecorder()
	app.Chat.Redeem(rdRec2, rdReq2)
	if rdRec2.Code != http.StatusBadRequest {
		t.Errorf("reuse topup: want 400, got %d", rdRec2.Code)
	}
}
