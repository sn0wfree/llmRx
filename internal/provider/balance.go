package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Upstream balance probing.
//
// Unlike chat/completions, balance endpoints are not standardised: each
// upstream exposes a different path and returns a different shape.
// DeepSeek/Moonshot/SiliconFlow all speak the OpenAI API but put their
// balance under /user/balance, /users/me/balance and /user/info
// respectively. So dispatch is by provider name (via the descriptor
// registry), not by protocol.
//
// See docs/P14-BALANCE-MONITOR.md for the endpoint survey.

// Balance is a normalised snapshot of an upstream account.
type Balance struct {
	// Available is the spendable amount. Units follow Currency.
	Available float64 `json:"available"`
	// Currency is ISO 4217 when the upstream reports it, otherwise ""
	// (site-dependent: most Chinese providers bill in CNY).
	Currency string `json:"currency"`
	// Granted is the promotional/credit portion when reported separately.
	Granted float64 `json:"granted,omitempty"`
	// Total is the reported total when the upstream distinguishes it
	// from the spendable amount. 0 means "not reported".
	Total float64 `json:"total,omitempty"`
	// Source names the parser that produced this snapshot ("deepseek",
	// "oneapi", ...), for debugging a surprising number.
	Source string `json:"source"`
	// Note carries a non-fatal upstream remark, e.g. an account marked
	// unavailable. Empty on a clean read.
	Note string `json:"note,omitempty"`
}

// BalanceSpec declares how to reach a provider's balance endpoint.
// Populated from the provider descriptor (providers.yaml) or overridden
// per channel via channel.balance_url.
type BalanceSpec struct {
	// Path is appended to the provider's API root (the base URL with a
	// trailing /v1 removed, since balance endpoints usually live beside
	// the versioned API rather than inside it).
	Path string `yaml:"path" json:"path"`
	// Format selects a registered parser. See balanceParsers.
	Format string `yaml:"format" json:"format"`
}

// balanceParser turns one upstream response body into a Balance.
// Returning an error means "this response is not what we expected",
// which surfaces as a query failure rather than a silent zero.
type balanceParser func(body []byte) (Balance, error)

var (
	balanceMu       sync.RWMutex
	balanceParsers  = map[string]balanceParser{}
	balanceTimeout  = 10 * time.Second
	balanceHTTPDoer = func() *http.Client { return &http.Client{Timeout: balanceTimeout} }
)

// RegisterBalanceParser makes a response format available by name. The
// built-in formats register below; extra ones can be added for a
// private upstream without touching the dispatch logic.
func RegisterBalanceParser(name string, p balanceParser) {
	balanceMu.Lock()
	defer balanceMu.Unlock()
	balanceParsers[strings.ToLower(name)] = p
}

func lookupBalanceParser(name string) balanceParser {
	balanceMu.RLock()
	defer balanceMu.RUnlock()
	return balanceParsers[strings.ToLower(name)]
}

// balanceRoot returns the API root a balance path hangs off: the base
// URL with any trailing "/v1" removed. DeepSeek's balance lives at
// https://api.deepseek.com/user/balance, not under /v1.
func balanceRoot(baseURL string) string {
	root := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(root, "/v1") {
		root = strings.TrimSuffix(root, "/v1")
	}
	return root
}

// QueryBalance fetches and parses the upstream balance for a channel.
//
// baseURL is the channel's configured base URL; overrideURL, when
// non-empty, is used verbatim (channel.balance_url). Dispatch:
//
//  1. overrideURL + the provider's format, else
//  2. the provider descriptor's balance path + format.
//
// A provider with no balance spec returns ErrBalanceUnsupported so the
// caller can render "not supported" instead of an error.
func QueryBalance(ctx context.Context, providerName, apiKey, baseURL, overrideURL string) (Balance, error) {
	if strings.TrimSpace(apiKey) == "" {
		return Balance{}, errors.New("balance: empty api key")
	}

	desc, hasDesc := LookupProvider(providerName)
	format := ""
	if hasDesc {
		format = desc.Balance.Format
	}

	var endpoint string
	switch {
	case overrideURL != "":
		endpoint = overrideURL
		if format == "" {
			// An override URL with no declared format is only usable
			// if the operator also named one; fall back to the
			// provider's, which we already read.
			return Balance{}, fmt.Errorf("balance: channel overrides balance_url but provider %q declares no format", providerName)
		}
	case hasDesc && desc.Balance.Path != "":
		endpoint = balanceRoot(baseURL) + desc.Balance.Path
	default:
		return Balance{}, ErrBalanceUnsupported
	}

	parse := lookupBalanceParser(format)
	if parse == nil {
		return Balance{}, fmt.Errorf("balance: unknown format %q for provider %q", format, providerName)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Balance{}, fmt.Errorf("balance: build request: %w", err)
	}
	// Every upstream surveyed authenticates balance reads with the same
	// bearer header as chat.
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := balanceHTTPDoer().Do(req)
	if err != nil {
		return Balance{}, fmt.Errorf("balance: request: %w", err)
	}
	defer resp.Body.Close()

	// Bound the read: a balance document is a few hundred bytes; 1 MiB
	// is generous and prevents a misbehaving upstream from streaming
	// forever.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Balance{}, fmt.Errorf("balance: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Balance{}, fmt.Errorf("balance: upstream status %d: %s",
			resp.StatusCode, truncateForError(body, 200))
	}

	b, err := parse(body)
	if err != nil {
		return Balance{}, fmt.Errorf("balance: parse: %w", err)
	}
	b.Source = format
	return b, nil
}

// ErrBalanceUnsupported is returned when a provider declares no
// balance endpoint. Callers render "not supported" rather than an error.
var ErrBalanceUnsupported = errors.New("balance: provider does not support balance queries")

// SetBalanceHTTPClient overrides the client used for balance requests.
// Intended for tests (httptest servers) — production uses the default.
func SetBalanceHTTPClient(c *http.Client) {
	if c == nil {
		balanceHTTPDoer = func() *http.Client { return &http.Client{Timeout: balanceTimeout} }
		balanceMu.Lock()
		balanceTimeout = 10 * time.Second
		balanceMu.Unlock()
		return
	}
	balanceHTTPDoer = func() *http.Client { return c }
}

func truncateForError(b []byte, max int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// ---------- Built-in parsers ----------
//
// Each parser hard-codes the field it reads. Upstreams have already
// shipped responses where the "obvious" field was the wrong one
// (SiliconFlow: totalBalance vs balance), so the choice belongs in code
// with a fixture test, not in configuration.

func init() {
	RegisterBalanceParser("deepseek", parseDeepSeekBalance)
	RegisterBalanceParser("moonshot", parseMoonshotBalance)
	RegisterBalanceParser("zhipu", parseZhipuBalance)
	RegisterBalanceParser("siliconflow", parseSiliconFlowBalance)
	RegisterBalanceParser("oneapi", parseOneAPIBalance)
}

// parseDeepSeekBalance handles GET {root}/user/balance:
//
//	{"is_available":true,"balance_infos":[
//	   {"currency":"CNY","total_balance":"110.00",
//	    "granted_balance":"10.00","topped_up_balance":"100.00"}]}
func parseDeepSeekBalance(body []byte) (Balance, error) {
	var r struct {
		IsAvailable  bool `json:"is_available"`
		BalanceInfos []struct {
			Currency      string `json:"currency"`
			TotalBalance  string `json:"total_balance"`
			GrantedBaance string `json:"granted_balance"`
			ToppedUp      string `json:"topped_up_balance"`
		} `json:"balance_infos"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Balance{}, err
	}
	if len(r.BalanceInfos) == 0 {
		return Balance{}, errors.New("deepseek: no balance_infos in response")
	}
	info := r.BalanceInfos[0]
	total, err := strconv.ParseFloat(strings.TrimSpace(info.TotalBalance), 64)
	if err != nil {
		return Balance{}, fmt.Errorf("deepseek: total_balance %q: %w", info.TotalBalance, err)
	}
	granted, _ := strconv.ParseFloat(strings.TrimSpace(info.GrantedBaance), 64)
	b := Balance{
		Available: total,
		Total:     total,
		Granted:   granted,
		Currency:  strings.ToUpper(strings.TrimSpace(info.Currency)),
	}
	if !r.IsAvailable {
		b.Note = "account reported unavailable by upstream"
	}
	return b, nil
}

// parseMoonshotBalance handles GET {base}/users/me/balance:
//
//	{"code":0,"data":{"available_balance":"12590.00",
//	                  "voucher_balance":"0.00","cash_balance":"12590.00"}}
func parseMoonshotBalance(body []byte) (Balance, error) {
	var r struct {
		Code int `json:"code"`
		Data struct {
			Available string `json:"available_balance"`
			Voucher   string `json:"voucher_balance"`
			Cash      string `json:"cash_balance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Balance{}, err
	}
	if r.Code != 0 {
		return Balance{}, fmt.Errorf("moonshot: upstream code %d", r.Code)
	}
	avail, err := strconv.ParseFloat(strings.TrimSpace(r.Data.Available), 64)
	if err != nil {
		return Balance{}, fmt.Errorf("moonshot: available_balance %q: %w", r.Data.Available, err)
	}
	voucher, _ := strconv.ParseFloat(strings.TrimSpace(r.Data.Voucher), 64)
	cash, _ := strconv.ParseFloat(strings.TrimSpace(r.Data.Cash), 64)
	return Balance{Available: avail, Granted: voucher, Total: cash + voucher}, nil
}

// parseZhipuBalance handles GET {base}/users/me/balance. The public
// documentation does not pin the exact field names, so this accepts the
// shapes seen in the wild and reports a clear error otherwise.
func parseZhipuBalance(body []byte) (Balance, error) {
	var r struct {
		Code    *int   `json:"code"`
		Msg     string `json:"msg"`
		Data    json.RawMessage `json:"data"`
		Balance *float64 `json:"balance"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Balance{}, err
	}
	if r.Code != nil && *r.Code != 0 && *r.Code != 200 {
		return Balance{}, fmt.Errorf("zhipu: upstream code %d: %s", *r.Code, r.Msg)
	}
	if r.Balance != nil {
		return Balance{Available: *r.Balance}, nil
	}
	if len(r.Data) == 0 {
		return Balance{}, errors.New("zhipu: response has neither balance nor data")
	}
	// data may be an object with a balance field, or a bare number.
	var obj struct {
		Balance     *float64 `json:"balance"`
		Available   *float64 `json:"available_balance"`
		Total       *float64 `json:"total_balance"`
	}
	if err := json.Unmarshal(r.Data, &obj); err == nil {
		switch {
		case obj.Balance != nil:
			return Balance{Available: *obj.Balance}, nil
		case obj.Available != nil:
			return Balance{Available: *obj.Available}, nil
		case obj.Total != nil:
			return Balance{Available: *obj.Total, Total: *obj.Total}, nil
		}
	}
	var num float64
	if err := json.Unmarshal(r.Data, &num); err == nil {
		return Balance{Available: num}, nil
	}
	return Balance{}, fmt.Errorf("zhipu: unrecognised data shape: %s", truncateForError(r.Data, 200))
}

// parseSiliconFlowBalance handles GET {base}/user/info.
//
// Reads "balance" (spendable), NOT "totalBalance": the latter includes
// non-spendable grants and has caused tools to display negative or
// inflated figures.
//
// The numeric fields arrive as JSON numbers on the public API but as
// quoted strings on some deployments, so they are decoded through
// flexFloat rather than a bare float64.
func parseSiliconFlowBalance(body []byte) (Balance, error) {
	var r struct {
		Data struct {
			Balance      flexFloat `json:"balance"`
			ChargeBal    flexFloat `json:"chargeBalance"`
			TotalBalance flexFloat `json:"totalBalance"`
			Status       string    `json:"status"`
			Message      string    `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Balance{}, err
	}
	if !r.Data.Balance.set {
		return Balance{}, errors.New("siliconflow: no balance field in response")
	}
	b := Balance{Available: r.Data.Balance.value}
	if r.Data.TotalBalance.set {
		b.Total = r.Data.TotalBalance.value
	}
	if s := strings.TrimSpace(r.Data.Status); s != "" && !strings.EqualFold(s, "normal") {
		b.Note = "account status: " + s
	}
	return b, nil
}

// flexFloat accepts a JSON number or a numeric string. Upstreams are
// inconsistent about which they send for money fields.
type flexFloat struct {
	value float64
	set   bool
}

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		return nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	f.value = v
	f.set = true
	return nil
}

// parseOneAPIBalance handles the one-api / new-api billing pair:
//
//	GET {root}/dashboard/billing/subscription  → {"hard_limit_usd":110}
//	GET {root}/dashboard/billing/usage         → {"total_usage":2500}   (cents)
//
// Available = hard_limit_usd - total_usage/100. one-api reports both in
// USD, so no currency guessing is needed.
//
// This parser needs two documents; QueryBalance only fetches one. The
// balance monitor therefore calls QueryOneAPIBalance for one-api
// providers instead. Kept registered so the format name resolves.
func parseOneAPIBalance(body []byte) (Balance, error) {
	var r struct {
		HardLimitUSD *float64 `json:"hard_limit_usd"`
		TotalUsage   *float64 `json:"total_usage"`
		// new-api also exposes a direct remaining figure in some builds.
		Remaining *float64 `json:"remaining_usd"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Balance{}, err
	}
	if r.Remaining != nil {
		return Balance{Available: *r.Remaining, Currency: "USD", Total: *r.Remaining}, nil
	}
	if r.HardLimitUSD != nil && r.TotalUsage != nil {
		avail := *r.HardLimitUSD - *r.TotalUsage/100
		return Balance{Available: avail, Currency: "USD", Total: *r.HardLimitUSD}, nil
	}
	return Balance{}, errors.New("oneapi: response lacks hard_limit_usd/total_usage")
}

// QueryOneAPIBalance performs the two-call one-api/new-api balance read.
// usageSinceUnix limits the usage window (one-api expects a start_date
// epoch); 90 days is the practical default.
func QueryOneAPIBalance(ctx context.Context, apiKey, baseURL, overrideURL string, usageSinceUnix int64) (Balance, error) {
	if strings.TrimSpace(apiKey) == "" {
		return Balance{}, errors.New("balance: empty api key")
	}
	root := balanceRoot(baseURL)
	if overrideURL != "" {
		// An override points at subscription; usage sits beside it.
		root = strings.TrimSuffix(overrideURL, "/dashboard/billing/subscription")
	}

	sub, err := balanceGET(ctx, root+"/dashboard/billing/subscription", apiKey)
	if err != nil {
		return Balance{}, fmt.Errorf("oneapi: subscription: %w", err)
	}
	usageURL := fmt.Sprintf("%s/dashboard/billing/usage?start_date=%d&end_date=%d",
		root, usageSinceUnix, time.Now().Unix())
	usage, err := balanceGET(ctx, usageURL, apiKey)
	if err != nil {
		return Balance{}, fmt.Errorf("oneapi: usage: %w", err)
	}

	var subDoc struct {
		HardLimitUSD *float64 `json:"hard_limit_usd"`
		Remaining    *float64 `json:"remaining_usd"`
		SystemHard   *float64 `json:"system_hard_limit_usd"`
	}
	if err := json.Unmarshal(sub, &subDoc); err != nil {
		return Balance{}, fmt.Errorf("oneapi: parse subscription: %w", err)
	}
	var usageDoc struct {
		TotalUsage *float64 `json:"total_usage"`
	}
	if err := json.Unmarshal(usage, &usageDoc); err != nil {
		return Balance{}, fmt.Errorf("oneapi: parse usage: %w", err)
	}

	if subDoc.Remaining != nil {
		return Balance{Available: *subDoc.Remaining, Currency: "USD", Total: *subDoc.Remaining, Source: "oneapi"}, nil
	}
	limit := subDoc.HardLimitUSD
	if limit == nil {
		limit = subDoc.SystemHard
	}
	if limit == nil {
		return Balance{}, errors.New("oneapi: subscription lacks hard_limit_usd")
	}
	used := 0.0
	if usageDoc.TotalUsage != nil {
		used = *usageDoc.TotalUsage / 100 // response is in cents
	}
	return Balance{
		Available: *limit - used,
		Total:     *limit,
		Currency:  "USD",
		Source:    "oneapi",
	}, nil
}

// balanceGET is the shared single-request helper.
func balanceGET(ctx context.Context, url, apiKey string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := balanceHTTPDoer().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncateForError(body, 200))
	}
	return body, nil
}
