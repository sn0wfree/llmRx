package admin_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sn0wfree/llmRx/internal/balance"
	"github.com/sn0wfree/llmRx/internal/model"
	"github.com/sn0wfree/llmRx/internal/provider"
	"github.com/sn0wfree/llmRx/internal/store"
	"github.com/sn0wfree/llmRx/internal/testhelper"
)

// fakeBalance implements admin.BalanceRefresher. Like the real monitor,
// a successful query persists the snapshot onto the channel row and a
// failed one records the error while leaving the previous balance
// intact — that contract is what the endpoints and the UI both rely on.
type fakeBalance struct {
	store store.Store
	b     provider.Balance
	err   error
	last  *model.Channel
}

func (f *fakeBalance) QueryChannel(_ context.Context, ch *model.Channel) (provider.Balance, error) {
	f.last = ch
	now := time.Now().UTC()
	if f.err != nil {
		_ = f.store.UpdateChannelBalance(ch.ID, "", "", now, f.err.Error())
		return provider.Balance{}, f.err
	}
	_ = f.store.UpdateChannelBalance(ch.ID, balance.FormatAmount(f.b.Available), f.b.Currency, now, "")
	return f.b, nil
}

func TestListBalances_Empty(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	rec := do(t, app.Admin.Routes(), http.MethodGet, "/balances", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Balances []map[string]any `json:"balances"`
		Totals   map[string]any   `json:"totals"`
	}
	decodeJSON(t, rec, &resp)
	if len(resp.Balances) != 0 {
		t.Fatalf("balances = %v, want empty", resp.Balances)
	}
	if resp.Totals == nil {
		t.Fatal("totals must be present (empty object), not null")
	}
}

// Unsupported providers still appear, flagged so the UI renders "—"
// instead of an empty cell. This is the difference between "we queried
// and got nothing" and "we cannot query at all".
func TestListBalances_MarksUnsupported(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)

	app.AddChannel("gpt", "openai", "https://api.openai.com/v1", []string{"gpt-4"}, "sk-a")
	app.AddChannel("ds", "deepseek", "https://api.deepseek.com/v1", []string{"deepseek-chat"}, "sk-b")

	now := time.Now().UTC()
	if err := app.Store.UpdateChannelBalance(2, "110", "CNY", now, ""); err != nil {
		t.Fatalf("seed balance: %v", err)
	}

	rec := do(t, app.Admin.Routes(), http.MethodGet, "/balances", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Balances []struct {
			Name      string `json:"name"`
			Balance   string `json:"balance"`
			Supported bool   `json:"supported"`
		} `json:"balances"`
		Totals map[string]float64 `json:"totals"`
	}
	decodeJSON(t, rec, &resp)

	byName := map[string]string{}
	supported := map[string]bool{}
	for _, b := range resp.Balances {
		byName[b.Name] = b.Balance
		supported[b.Name] = b.Supported
	}
	if supported["gpt"] {
		t.Fatal("openai should be reported as unsupported")
	}
	if !supported["ds"] || byName["ds"] != "110" {
		t.Fatalf("deepseek row = balance %q supported %v", byName["ds"], supported["ds"])
	}
	// Totals are per currency: 110 CNY and 110 USD are not the same money.
	if got := resp.Totals["CNY"]; got != 110 {
		t.Fatalf("totals[CNY] = %v, want 110", got)
	}
}

func TestRefreshChannelBalance_NotConfigured(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("ds", "deepseek", "https://api.deepseek.com/v1", []string{"deepseek-chat"}, "sk-a")

	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels/1/balance", sess, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestRefreshChannelBalance_NotFound(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.Admin.SetBalanceMonitor(&fakeBalance{store: app.Store})

	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels/999/balance", sess, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRefreshChannelBalance_Unsupported(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("ds", "deepseek", "https://api.deepseek.com/v1", []string{"deepseek-chat"}, "sk-a")
	app.Admin.SetBalanceMonitor(&fakeBalance{store: app.Store, err: provider.ErrBalanceUnsupported})

	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels/1/balance", sess, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestRefreshChannelBalance_UpstreamError(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("ds", "deepseek", "https://api.deepseek.com/v1", []string{"deepseek-chat"}, "sk-a")
	app.Admin.SetBalanceMonitor(&fakeBalance{store: app.Store, err: errors.New("upstream 503")})

	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels/1/balance", sess, "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}

	// The failure must be visible on the channel row.
	ch, err := app.Store.GetChannel(1)
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if ch.BalanceError == "" {
		t.Fatal("error was not persisted")
	}
	if ch.Balance != "" {
		t.Fatalf("balance should be untouched on failure, got %q", ch.Balance)
	}
}

// The success path returns the refreshed values read from the channel
// row the monitor updated — not the in-memory copy the handler passed.
func TestRefreshChannelBalance_Success(t *testing.T) {
	app := testhelper.New(t)
	sess := login(t, app)
	app.AddChannel("ds", "deepseek", "https://api.deepseek.com/v1", []string{"deepseek-chat"}, "sk-a")
	app.Admin.SetBalanceMonitor(&fakeBalance{
		store: app.Store,
		b:     provider.Balance{Available: 42.5, Currency: "CNY", Source: "deepseek"},
	})

	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels/1/balance", sess, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ChannelID int64  `json:"channel_id"`
		Balance   string `json:"balance"`
		Currency  string `json:"currency"`
		Source    string `json:"source"`
	}
	decodeJSON(t, rec, &resp)
	if resp.Balance != "42.5" || resp.Currency != "CNY" || resp.Source != "deepseek" {
		t.Fatalf("resp = %+v", resp)
	}

	// The persisted snapshot must survive a fresh read.
	ch, err := app.Store.GetChannel(1)
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if ch.Balance != "42.5" || ch.BalanceCurrency != "CNY" || ch.BalanceUpdatedAt == nil {
		t.Fatalf("persisted snapshot wrong: %+v", ch)
	}
	if ch.BalanceError != "" {
		t.Fatalf("stale error left behind: %q", ch.BalanceError)
	}
}

// Routes must be session-guarded like every other admin endpoint.
func TestRefreshChannelBalance_RequiresSession(t *testing.T) {
	app := testhelper.New(t)
	app.Admin.SetBalanceMonitor(&fakeBalance{store: app.Store})

	rec := do(t, app.Admin.Routes(), http.MethodPost, "/channels/1/balance", "", "")
	if rec.Code == http.StatusOK {
		t.Fatalf("unauthenticated request returned %d", rec.Code)
	}
}
