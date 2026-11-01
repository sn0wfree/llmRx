package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The parser fixtures are verbatim response shapes from the provider
// docs. They exist so a schema change upstream surfaces here instead of
// as a silently wrong number on the admin dashboard. Field selection is
// deliberate (see balance.go), so these assertions are the spec.

func TestParseDeepSeekBalance(t *testing.T) {
	body := []byte(`{
		"is_available": true,
		"balance_infos": [
			{"currency": "CNY", "total_balance": "110.00",
			 "granted_balance": "10.00", "topped_up_balance": "100.00"}
		]
	}`)
	b, err := parseDeepSeekBalance(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if b.Available != 110 {
		t.Fatalf("Available = %v, want 110", b.Available)
	}
	if b.Granted != 10 {
		t.Fatalf("Granted = %v, want 10", b.Granted)
	}
	if b.Currency != "CNY" {
		t.Fatalf("Currency = %q, want CNY", b.Currency)
	}
	if b.Note != "" {
		t.Fatalf("unexpected Note: %q", b.Note)
	}
}

func TestParseDeepSeekBalance_Unavailable(t *testing.T) {
	body := []byte(`{"is_available": false, "balance_infos": [
		{"currency":"USD","total_balance":"0.00","granted_balance":"0.00","topped_up_balance":"0.00"}]}`)
	b, err := parseDeepSeekBalance(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if b.Available != 0 {
		t.Fatalf("Available = %v, want 0", b.Available)
	}
	if b.Note == "" {
		t.Fatal("expected a note for an unavailable account")
	}
}

func TestParseDeepSeekBalance_Errors(t *testing.T) {
	cases := map[string]string{
		"no infos":   `{"is_available":true,"balance_infos":[]}`,
		"bad number": `{"is_available":true,"balance_infos":[{"total_balance":"abc"}]}`,
		"not json":   `nope`,
	}
	for name, body := range cases {
		if _, err := parseDeepSeekBalance([]byte(body)); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestParseMoonshotBalance(t *testing.T) {
	body := []byte(`{"code":0,"data":{
		"available_balance":"12590.00",
		"voucher_balance":"90.00",
		"cash_balance":"12500.00"}}`)
	b, err := parseMoonshotBalance(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if b.Available != 12590 {
		t.Fatalf("Available = %v, want 12590", b.Available)
	}
	if b.Granted != 90 {
		t.Fatalf("Granted = %v, want 90", b.Granted)
	}
	// Total is cash + voucher, which must equal available for a
	// consistent upstream response.
	if b.Total != 12590 {
		t.Fatalf("Total = %v, want 12590", b.Total)
	}
}

func TestParseMoonshotBalance_UpstreamErrorCode(t *testing.T) {
	body := []byte(`{"code":401,"data":{}}`)
	if _, err := parseMoonshotBalance(body); err == nil {
		t.Fatal("expected error on non-zero upstream code")
	}
}

func TestParseZhipuBalance_Forms(t *testing.T) {
	cases := []struct {
		name string
		body string
		want float64
	}{
		{"top-level balance", `{"code":200,"balance":42.5}`, 42.5},
		{"data.balance", `{"code":200,"data":{"balance":42.5}}`, 42.5},
		{"data.available_balance", `{"code":200,"data":{"available_balance":42.5}}`, 42.5},
		{"data.total_balance", `{"code":200,"data":{"total_balance":42.5}}`, 42.5},
		{"data numeric", `{"code":200,"data":42.5}`, 42.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := parseZhipuBalance([]byte(tc.body))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if b.Available != tc.want {
				t.Fatalf("Available = %v, want %v", b.Available, tc.want)
			}
		})
	}
}

func TestParseZhipuBalance_Errors(t *testing.T) {
	cases := map[string]string{
		"error code":   `{"code":401,"msg":"bad key"}`,
		"empty":        `{"code":200}`,
		"unknown data": `{"code":200,"data":{"weird":"x"}}`,
	}
	for name, body := range cases {
		if _, err := parseZhipuBalance([]byte(body)); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

// SiliconFlow is the case that motivated hard-coding field choice:
// "balance" is spendable, "totalBalance" includes non-spendable grants
// and has produced negative figures in other tools.
func TestParseSiliconFlowBalance_UsesSpendableBalance(t *testing.T) {
	body := []byte(`{"code":200,"data":{
		"balance":"10.00","chargeBalance":"8.00","totalBalance":"99.00","status":"normal"}}`)
	b, err := parseSiliconFlowBalance(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if b.Available != 10 {
		t.Fatalf("Available = %v, want 10 (the spendable balance, not totalBalance)", b.Available)
	}
	if b.Total != 99 {
		t.Fatalf("Total = %v, want 99", b.Total)
	}
	if b.Note != "" {
		t.Fatalf("unexpected Note: %q", b.Note)
	}
}

func TestParseSiliconFlowBalance_StringBalance(t *testing.T) {
	body := []byte(`{"data":{"balance":"7.5","status":"normal"}}`)
	b, err := parseSiliconFlowBalance(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if b.Available != 7.5 {
		t.Fatalf("Available = %v, want 7.5", b.Available)
	}
}

func TestParseSiliconFlowBalance_AbnormalStatus(t *testing.T) {
	body := []byte(`{"data":{"balance":1.0,"status":"frozen"}}`)
	b, err := parseSiliconFlowBalance(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if b.Note == "" {
		t.Fatal("expected a note for a non-normal account status")
	}
}

func TestParseSiliconFlowBalance_NoBalance(t *testing.T) {
	if _, err := parseSiliconFlowBalance([]byte(`{"data":{"status":"normal"}}`)); err == nil {
		t.Fatal("expected error when balance is absent")
	}
}

func TestParseOneAPIBalance_Combined(t *testing.T) {
	body := []byte(`{"hard_limit_usd":110.0,"total_usage":2500}`)
	b, err := parseOneAPIBalance(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// 110 - 2500/100 = 85
	if b.Available != 85 {
		t.Fatalf("Available = %v, want 85", b.Available)
	}
	if b.Currency != "USD" {
		t.Fatalf("Currency = %q, want USD", b.Currency)
	}
}

func TestParseOneAPIBalance_Remaining(t *testing.T) {
	b, err := parseOneAPIBalance([]byte(`{"remaining_usd":12.5}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if b.Available != 12.5 {
		t.Fatalf("Available = %v, want 12.5", b.Available)
	}
}

func TestBalanceRoot(t *testing.T) {
	cases := map[string]string{
		"https://api.deepseek.com/v1":        "https://api.deepseek.com",
		"https://api.deepseek.com/v1/":       "https://api.deepseek.com",
		"https://api.deepseek.com":           "https://api.deepseek.com",
		"https://api.deepseek.com/":          "https://api.deepseek.com",
		"https://open.bigmodel.cn/api/paas/v4": "https://open.bigmodel.cn/api/paas/v4",
	}
	for in, want := range cases {
		if got := balanceRoot(in); got != want {
			t.Fatalf("balanceRoot(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---------- QueryBalance dispatch ----------

func TestQueryBalance_DispatchesByProviderName(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"is_available":true,"balance_infos":[
			{"currency":"CNY","total_balance":"5.00","granted_balance":"0","topped_up_balance":"5.00"}]}`))
	}))
	defer srv.Close()

	SetBalanceHTTPClient(srv.Client())
	defer SetBalanceHTTPClient(nil)

	b, err := QueryBalance(context.Background(), "deepseek", "sk-test", srv.URL+"/v1", "")
	if err != nil {
		t.Fatalf("QueryBalance: %v", err)
	}
	if b.Available != 5 {
		t.Fatalf("Available = %v, want 5", b.Available)
	}
	if b.Source != "deepseek" {
		t.Fatalf("Source = %q, want deepseek", b.Source)
	}
	// The path must hang off the API root, with /v1 stripped.
	if gotPath != "/user/balance" {
		t.Fatalf("requested path = %q, want /user/balance", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
}

func TestQueryBalance_OverrideURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"data":{"balance":"3.00","status":"normal"}}`))
	}))
	defer srv.Close()

	SetBalanceHTTPClient(srv.Client())
	defer SetBalanceHTTPClient(nil)

	b, err := QueryBalance(context.Background(), "siliconflow", "sk-x", "https://ignored.example/v1", srv.URL+"/custom/info")
	if err != nil {
		t.Fatalf("QueryBalance: %v", err)
	}
	if b.Available != 3 {
		t.Fatalf("Available = %v, want 3", b.Available)
	}
	if gotPath != "/custom/info" {
		t.Fatalf("override URL ignored: requested %q", gotPath)
	}
}

func TestQueryBalance_UnsupportedProvider(t *testing.T) {
	_, err := QueryBalance(context.Background(), "openai", "sk-x", "https://api.openai.com/v1", "")
	if !errors.Is(err, ErrBalanceUnsupported) {
		t.Fatalf("err = %v, want ErrBalanceUnsupported", err)
	}
}

func TestQueryBalance_EmptyKey(t *testing.T) {
	if _, err := QueryBalance(context.Background(), "deepseek", "", "https://api.deepseek.com/v1", ""); err == nil {
		t.Fatal("expected error for empty api key")
	}
}

func TestQueryBalance_UpstreamHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid key"}`))
	}))
	defer srv.Close()

	SetBalanceHTTPClient(srv.Client())
	defer SetBalanceHTTPClient(nil)

	_, err := QueryBalance(context.Background(), "deepseek", "sk-bad", srv.URL+"/v1", "")
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error should mention the status: %v", err)
	}
}

func TestQueryBalance_UnknownFormat(t *testing.T) {
	RegisterProvider(ProviderDesc{Name: "weird", Protocol: "openai", Balance: BalanceSpec{Path: "/b", Format: "nope"}})
	_, err := QueryBalance(context.Background(), "weird", "sk-x", "https://x.example", "")
	if err == nil || !strings.Contains(err.Error(), "unknown format") {
		t.Fatalf("err = %v, want unknown format", err)
	}
}

func TestRegisterBalanceParser_Overrides(t *testing.T) {
	RegisterBalanceParser("testfmt", func(body []byte) (Balance, error) {
		return Balance{Available: 1}, nil
	})
	p := lookupBalanceParser("TESTFMT") // case-insensitive
	if p == nil {
		t.Fatal("parser not registered")
	}
	b, err := p(nil)
	if err != nil || b.Available != 1 {
		t.Fatalf("override parser not used: %v %v", b, err)
	}
}
