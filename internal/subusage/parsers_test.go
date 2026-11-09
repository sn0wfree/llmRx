package subusage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// withStubServer points the package HTTP client at a test server for
// the duration of fn.
func withStubServer(t *testing.T, h http.HandlerFunc, fn func(base string)) {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()
	prev := httpDoer
	httpDoer = func() *http.Client { return srv.Client() }
	t.Cleanup(func() { httpDoer = prev })
	fn(srv.URL)
}

func TestOpenRouterKey_ParsesLimitsAndBudgets(t *testing.T) {
	body := `{"data":{
		"label":"prod-key",
		"limit":100.0,
		"limit_remaining":25.0,
		"usage":75.0,
		"usage_daily":1.5,
		"usage_weekly":12.0,
		"usage_monthly":75.0,
		"is_free_tier":false,
		"budgets":[
			{"scope":"api_key","limit_usd":10,"spend_usd":4,"remaining_usd":6,
			 "reset_interval":"weekly","resets_at":"2026-12-01T00:00:00Z"},
			{"scope":"workspace","limit_usd":50,"spend_usd":20,"remaining_usd":30,
			 "reset_interval":"monthly","resets_at":"2026-12-01T00:00:00Z"}
		]
	}}`
	withStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-or-test" {
			t.Errorf("auth header = %q", got)
		}
		if r.URL.Path != "/api/v1/key" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	}, func(base string) {
		rep, err := Probe(context.Background(), "openrouter_key", Input{
			APIKey:  "sk-or-test",
			BaseURL: base + "/api/v1",
		})
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if rep.Source != "openrouter_key" {
			t.Fatalf("source = %q", rep.Source)
		}
		if rep.Experimental {
			t.Fatal("OpenRouter is a documented API, not experimental")
		}

		key := rep.Window(WindowKeyLimit)
		if key == nil {
			t.Fatalf("missing key_limit window: %+v", rep.Points)
		}
		if key.Used != 75 || key.Limit != 100 || key.Remaining != 25 {
			t.Fatalf("key_limit = %+v", key)
		}
		if got := key.Ratio(); got != 0.75 {
			t.Fatalf("key_limit ratio = %v", got)
		}

		weekly := rep.Window(WindowWeekly)
		if weekly == nil || weekly.Used != 4 || weekly.Limit != 10 {
			t.Fatalf("weekly budget = %+v", weekly)
		}
		if weekly.ResetsAt.IsZero() {
			t.Fatal("weekly budget lost resets_at")
		}
		monthly := rep.Window(WindowMonthly)
		if monthly == nil || monthly.Used != 20 {
			t.Fatalf("monthly budget = %+v", monthly)
		}
		// Tightest = key_limit at 75%.
		if tight := rep.Tightest(); tight == nil || tight.Window != WindowKeyLimit {
			t.Fatalf("tightest = %+v", tight)
		}
	})
}

func TestOpenRouterKey_UnlimitedKey(t *testing.T) {
	// null limit + remaining means "no key cap"; usage-only windows
	// must still surface.
	body := `{"data":{"limit":null,"limit_remaining":null,"usage":42.5,
		"usage_daily":0.5,"usage_weekly":3.0,"usage_monthly":42.5,"is_free_tier":false}}`
	withStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}, func(base string) {
		rep, err := Probe(context.Background(), "openrouter_key", Input{APIKey: "k", BaseURL: base + "/api/v1"})
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		key := rep.Window(WindowKeyLimit)
		if key == nil || key.Limit != 0 || key.Used != 42.5 {
			t.Fatalf("unlimited key window = %+v", key)
		}
		if key.Exhausted() {
			t.Fatal("an unlimited key must never read as exhausted")
		}
		if rep.Window(WindowDaily) == nil || rep.Window(WindowWeekly) == nil {
			t.Fatalf("usage-only windows missing: %+v", rep.Points)
		}
	})
}

func TestOpenRouterKey_FreeTierRequests(t *testing.T) {
	body := `{"data":{"usage":0,"is_free_tier":true,
		"free_model_daily_requests":{"used":10,"limit":50,"remaining":40}}}`
	withStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}, func(base string) {
		rep, err := Probe(context.Background(), "openrouter_key", Input{APIKey: "k", BaseURL: base + "/api/v1"})
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		var found *UsagePoint
		for i := range rep.Points {
			if rep.Points[i].Scope == "free_models" {
				found = &rep.Points[i]
			}
		}
		if found == nil {
			t.Fatalf("free-model quota missing: %+v", rep.Points)
		}
		if found.Unit != UnitRequests || found.Used != 10 || found.Limit != 50 {
			t.Fatalf("free-model window = %+v", found)
		}
		if rep.Warn == "" {
			t.Fatal("free tier should be flagged as a warning")
		}
	})
}

func TestOpenRouterKey_ExhaustedBudget(t *testing.T) {
	body := `{"data":{"limit":10,"limit_remaining":0,"usage":10,
		"budgets":[{"scope":"api_key","limit_usd":10,"spend_usd":10,"remaining_usd":0,"reset_interval":"daily"}]}}`
	withStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}, func(base string) {
		rep, err := Probe(context.Background(), "openrouter_key", Input{APIKey: "k", BaseURL: base + "/api/v1"})
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		tight := rep.Tightest()
		if tight == nil || !tight.Exhausted() || tight.Ratio() != 1 {
			t.Fatalf("tightest = %+v", tight)
		}
	})
}

func TestOpenRouterKey_HTTPError(t *testing.T) {
	withStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}, func(base string) {
		if _, err := Probe(context.Background(), "openrouter_key", Input{APIKey: "k", BaseURL: base + "/api/v1"}); err == nil {
			t.Fatal("expected an error for 401")
		}
	})
}

func TestZhipuWeb_HarvestsNestedWindows(t *testing.T) {
	body := `{"code":200,"data":{
		"fiveHourQuota":{"usedCredits":1200,"totalCredits":2000,"resetTime":"2026-12-01T10:00:00Z"},
		"weeklyQuota":{"usedCredits":5000,"totalCredits":10000,"resetAt":1764576000000}
	}}`
	withStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "sid=abc" {
			t.Errorf("cookie header = %q", r.Header.Get("Cookie"))
		}
		_, _ = w.Write([]byte(body))
	}, func(base string) {
		rep, err := Probe(context.Background(), "zhipu_web", Input{Cookie: "sid=abc", EndpointURL: base + "/usage"})
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if !rep.Experimental {
			t.Fatal("zhipu_web must be flagged experimental")
		}
		var rolling, weekly *UsagePoint
		for i := range rep.Points {
			switch rep.Points[i].Window {
			case WindowRolling:
				rolling = &rep.Points[i]
			case WindowWeekly:
				weekly = &rep.Points[i]
			}
		}
		if rolling == nil || rolling.Used != 1200 || rolling.Limit != 2000 {
			t.Fatalf("rolling window = %+v", rolling)
		}
		if rolling.ResetsAt.IsZero() {
			t.Fatal("rolling window lost its reset time")
		}
		if weekly == nil || weekly.Used != 5000 || weekly.Limit != 10000 {
			t.Fatalf("weekly window = %+v (ms timestamp not converted?)", weekly)
		}
	})
}

func TestZhipuWeb_EmbeddedJSON(t *testing.T) {
	html := `<!DOCTYPE html><html><body><script id="__NEXT_DATA__" type="application/json">` +
		`{"props":{"pageProps":{"usage":{"usedQuota":300,"totalQuota":1000}}}}` +
		`</script></body></html>`
	withStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(html))
	}, func(base string) {
		rep, err := Probe(context.Background(), "zhipu_web", Input{Cookie: "sid=abc", EndpointURL: base + "/page"})
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if len(rep.Points) == 0 {
			t.Fatalf("no window found in embedded JSON; warn=%q", rep.Warn)
		}
		if rep.Points[0].Used != 300 || rep.Points[0].Limit != 1000 {
			t.Fatalf("point = %+v", rep.Points[0])
		}
	})
}

func TestZhipuWeb_CookieRejected(t *testing.T) {
	withStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}, func(base string) {
		_, err := Probe(context.Background(), "zhipu_web", Input{Cookie: "bad", EndpointURL: base + "/usage"})
		if err == nil {
			t.Fatal("expected an error for a rejected cookie")
		}
	})
}

func TestZhipuWeb_NoCookie(t *testing.T) {
	if _, err := Probe(context.Background(), "zhipu_web", Input{}); err == nil {
		t.Fatal("cookie must be required")
	}
}

func TestZhipuWeb_UnrecognisedShapeIsWarnNotError(t *testing.T) {
	withStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"data":{"somethingElse":true}}`))
	}, func(base string) {
		rep, err := Probe(context.Background(), "zhipu_web", Input{Cookie: "c", EndpointURL: base + "/usage"})
		if err != nil {
			t.Fatalf("a shape change must not be an error: %v", err)
		}
		if rep.Warn == "" || len(rep.Points) != 0 {
			t.Fatalf("rep = %+v", rep)
		}
		if rep.Raw == "" {
			t.Fatal("raw excerpt must be kept for debugging")
		}
	})
}

func TestCustomParser_ObjectAndNumber(t *testing.T) {
	withStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"quota":{"used":30,"total":100,"remaining":70},"spend":9.5}}`))
	}, func(base string) {
		rep, err := Probe(context.Background(), "custom", Input{
			EndpointURL: base, JSONPath: "$.data.quota", WindowName: "monthly", Unit: UnitCredits,
		})
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if len(rep.Points) != 1 || rep.Points[0].Window != "monthly" ||
			rep.Points[0].Used != 30 || rep.Points[0].Limit != 100 {
			t.Fatalf("object extraction = %+v", rep.Points)
		}

		rep, err = Probe(context.Background(), "custom", Input{
			EndpointURL: base, JSONPath: "data.spend", WindowName: "credits", Unit: UnitUSD,
		})
		if err != nil {
			t.Fatalf("probe number: %v", err)
		}
		if len(rep.Points) != 1 || rep.Points[0].Used != 9.5 || rep.Points[0].Limit != 0 {
			t.Fatalf("number extraction = %+v", rep.Points)
		}
	})
}

func TestCustomParser_MissingPath(t *testing.T) {
	withStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{}}`))
	}, func(base string) {
		rep, err := Probe(context.Background(), "custom", Input{
			EndpointURL: base, JSONPath: "data.nope", WindowName: "w",
		})
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if rep.Warn == "" {
			t.Fatal("a missing path must produce a warning")
		}
	})
}

func TestLookupPath_ArrayIndex(t *testing.T) {
	var doc any
	if err := json.Unmarshal([]byte(`{"list":[{"used":1},{"used":2}]}`), &doc); err != nil {
		t.Fatal(err)
	}
	v, ok := lookupPath(doc, "list.1.used")
	if !ok || v.(float64) != 2 {
		t.Fatalf("lookup = %v %v", v, ok)
	}
	if _, ok := lookupPath(doc, "list.9.used"); ok {
		t.Fatal("out-of-range index must not match")
	}
	if _, ok := lookupPath(doc, "list.-1"); ok {
		t.Fatal("negative index must not match")
	}
}

func TestProbe_UnsupportedKind(t *testing.T) {
	if _, err := Probe(context.Background(), "nope", Input{}); err == nil {
		t.Fatal("expected ErrUnsupported")
	}
	if _, err := Probe(context.Background(), "", Input{}); err == nil {
		t.Fatal("empty kind must be rejected")
	}
}

func TestParserNamesContainsBuiltins(t *testing.T) {
	names := ParserNames()
	want := map[string]bool{"openrouter_key": false, "zhipu_web": false, "custom": false}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for n, found := range want {
		if !found {
			t.Fatalf("parser %q not registered (got %v)", n, names)
		}
	}
}

func TestUsagePoint_RatioPrefersRemaining(t *testing.T) {
	// Vendor usage lags behind remaining; remaining is authoritative.
	p := UsagePoint{Used: 10, Limit: 100, Remaining: 20}
	if got := p.Ratio(); got != 0.8 {
		t.Fatalf("ratio = %v, want 0.8 from remaining", got)
	}
	// No remaining reported → fall back to used/limit.
	p = UsagePoint{Used: 40, Limit: 100, Remaining: -1}
	if got := p.Ratio(); got != 0.4 {
		t.Fatalf("ratio = %v, want 0.4 from used", got)
	}
	// No limit → no ratio, never exhausted.
	p = UsagePoint{Used: 40, Remaining: -1}
	if got := p.Ratio(); got != 0 || p.Exhausted() {
		t.Fatalf("unbounded = %v %v", p.Ratio(), p.Exhausted())
	}
}

func TestNormalizeResetInterval(t *testing.T) {
	cases := map[string]string{
		"daily": WindowDaily, "DAY": WindowDaily, "weekly": WindowWeekly,
		"monthly": WindowMonthly, "yearly": "", "": "",
	}
	for in, want := range cases {
		if got := normalizeResetInterval(in); got != want {
			t.Fatalf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHTTPClientTimeoutIsSet(t *testing.T) {
	// Guards against a future refactor dropping the timeout — a vendor
	// that accepts the connection but never answers would otherwise hang
	// the sweep.
	SetHTTPClient(func() *http.Client { return &http.Client{Timeout: 3 * time.Second} })
	defer SetHTTPClient(nil)
	if c := httpDoer(); c.Timeout != 3*time.Second {
		t.Fatalf("timeout = %v", c.Timeout)
	}
	if c := httpDoer(); c.Timeout <= 0 {
		t.Fatal("restored client must have a positive timeout")
	}
}
