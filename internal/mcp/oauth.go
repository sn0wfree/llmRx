package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sn0wfree/llmRx/internal/logging"
	"github.com/sn0wfree/llmRx/internal/secrets"
	"github.com/sn0wfree/llmRx/internal/store"
)

// OAuth status values surfaced to the admin UI.
const (
	OAuthStatusPending     = "pending"     // not authorized yet
	OAuthStatusAuthorizing = "authorizing" // device flow in progress
	OAuthStatusReady       = "ready"       // valid access token available
	OAuthStatusFailed      = "failed"      // authorization errored
)

// oauthServerMetadata is the RFC 8414 authorization server
// metadata document.
type oauthServerMetadata struct {
	Issuer                      string `json:"issuer"`
	AuthorizationEndpoint       string `json:"authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
	RegistrationEndpoint        string `json:"registration_endpoint"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
}

// oauthTokenSet is the persisted token state.
type oauthTokenSet struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresAt    int64  `json:"expires_at"` // unix seconds; 0 = unknown
}

// oauthConfig is the admin-configured client settings.
type oauthConfig struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	Scopes       []string `json:"scopes"`
}

// OAuthState describes one server's authorization status for the UI.
type OAuthState struct {
	ServerID        int64  `json:"server_id"`
	ServerName      string `json:"server_name"`
	Status          string `json:"status"`
	UserCode        string `json:"user_code,omitempty"`
	VerificationURI string `json:"verification_uri,omitempty"`
	ExpiresIn       int    `json:"expires_in,omitempty"`
	ExpiresAt       int64  `json:"expires_at,omitempty"`
	Error           string `json:"error,omitempty"`
}

// oauthSession tracks one in-flight device flow.
type oauthSession struct {
	deviceCode   string
	userCode     string
	verification string
	expiresAt    time.Time
	interval     int
}

// OAuthManager drives OAuth 2.1 flows for MCP servers: discovery
// (RFC 8414), dynamic registration (RFC 7591), device flow
// (RFC 8628), and token refresh. Tokens are persisted on the server
// row, encrypted with the secrets manager when available.
type OAuthManager struct {
	repo    store.MCPRepository
	secrets *secrets.Manager
	client  *http.Client

	mu      sync.Mutex
	session map[int64]*oauthSession
}

// NewOAuthManager builds an OAuth manager backed by the MCP repo.
func NewOAuthManager(repo store.MCPRepository, sm *secrets.Manager) *OAuthManager {
	return &OAuthManager{
		repo:    repo,
		secrets: sm,
		client:  &http.Client{Timeout: 30 * time.Second},
		session: make(map[int64]*oauthSession),
	}
}

// NeedsOAuth reports whether a server has OAuth configured.
func (m *OAuthManager) NeedsOAuth(s *store.MCPServer) bool {
	return s != nil && s.OAuthConfigJSON != "" && s.OAuthConfigJSON != "{}"
}

// Status returns the current authorization status for a server.
func (m *OAuthManager) Status(ctx context.Context, serverID int64) (*OAuthState, error) {
	srv, err := m.repo.GetMCPServer(ctx, serverID)
	if err != nil || srv == nil {
		return nil, fmt.Errorf("mcp: server %d not found", serverID)
	}
	st := &OAuthState{ServerID: srv.ID, ServerName: srv.Name, Status: OAuthStatusPending}
	if !m.NeedsOAuth(srv) {
		st.Status = OAuthStatusPending
		return st, nil
	}
	if tok := m.decryptToken(srv.TokenJSON); tok != nil {
		if tok.AccessToken != "" {
			st.Status = OAuthStatusReady
			st.ExpiresAt = tok.ExpiresAt
			return st, nil
		}
	}
	m.mu.Lock()
	sess := m.session[serverID]
	m.mu.Unlock()
	if sess != nil {
		st.Status = OAuthStatusAuthorizing
		st.UserCode = sess.userCode
		st.VerificationURI = sess.verification
		st.ExpiresIn = int(time.Until(sess.expiresAt).Seconds())
	}
	return st, nil
}

// Authorize starts the device flow for a server. Returns the state
// with the user_code the operator must enter at the verification URI.
func (m *OAuthManager) Authorize(ctx context.Context, serverID int64) (*OAuthState, error) {
	srv, err := m.repo.GetMCPServer(ctx, serverID)
	if err != nil || srv == nil {
		return nil, fmt.Errorf("mcp: server %d not found", serverID)
	}
	if !m.NeedsOAuth(srv) {
		return nil, fmt.Errorf("mcp: server %s has no oauth config", srv.Name)
	}
	cfg, err := m.parseConfig(srv)
	if err != nil {
		return nil, err
	}
	meta, err := m.discover(ctx, srv.URL)
	if err != nil {
		return nil, err
	}
	if meta.DeviceAuthorizationEndpoint == "" {
		return nil, fmt.Errorf("mcp: server %s does not advertise a device_authorization_endpoint", srv.Name)
	}
	if cfg.ClientID == "" {
		if meta.RegistrationEndpoint == "" {
			return nil, fmt.Errorf("mcp: server %s has no client_id and no registration_endpoint", srv.Name)
		}
		if err := m.register(ctx, srv, meta, cfg); err != nil {
			return nil, err
		}
	}

	// Start the device flow.
	form := url.Values{}
	form.Set("client_id", cfg.ClientID)
	if len(cfg.Scopes) > 0 {
		form.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.DeviceAuthorizationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: oauth device request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mcp: oauth device %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var device struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := json.Unmarshal(body, &device); err != nil {
		return nil, fmt.Errorf("mcp: oauth device decode: %w", err)
	}
	interval := device.Interval
	if interval <= 0 {
		interval = 5
	}
	sess := &oauthSession{
		deviceCode:   device.DeviceCode,
		userCode:     device.UserCode,
		verification: device.VerificationURI,
		expiresAt:    time.Now().Add(time.Duration(device.ExpiresIn) * time.Second),
		interval:     interval,
	}
	m.mu.Lock()
	m.session[serverID] = sess
	m.mu.Unlock()

	return &OAuthState{
		ServerID:        srv.ID,
		ServerName:      srv.Name,
		Status:          OAuthStatusAuthorizing,
		UserCode:        device.UserCode,
		VerificationURI: device.VerificationURI,
		ExpiresIn:       device.ExpiresIn,
	}, nil
}

// Poll polls the token endpoint for a completed device flow. Callers
// invoke it repeatedly (typically every session.interval seconds).
func (m *OAuthManager) Poll(ctx context.Context, serverID int64) (*OAuthState, error) {
	srv, err := m.repo.GetMCPServer(ctx, serverID)
	if err != nil || srv == nil {
		return nil, fmt.Errorf("mcp: server %d not found", serverID)
	}
	cfg, err := m.parseConfig(srv)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	sess := m.session[serverID]
	m.mu.Unlock()
	if sess == nil {
		return m.Status(ctx, serverID)
	}
	meta, err := m.discover(ctx, srv.URL)
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	form.Set("device_code", sess.deviceCode)
	form.Set("client_id", cfg.ClientID)
	if cfg.ClientSecret != "" {
		form.Set("client_secret", cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: oauth token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var tokResp struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		TokenType        string `json:"token_type"`
		ExpiresIn        int    `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tokResp); err != nil {
		return nil, fmt.Errorf("mcp: oauth token decode: %w", err)
	}
	if tokResp.Error != "" {
		if tokResp.Error == "authorization_pending" {
			return &OAuthState{
				ServerID:        srv.ID,
				ServerName:      srv.Name,
				Status:          OAuthStatusAuthorizing,
				UserCode:        sess.userCode,
				VerificationURI: sess.verification,
				ExpiresIn:       int(time.Until(sess.expiresAt).Seconds()),
			}, nil
		}
		if tokResp.Error == "expired_token" || tokResp.Error == "access_denied" {
			m.mu.Lock()
			delete(m.session, serverID)
			m.mu.Unlock()
			return &OAuthState{
				ServerID:   srv.ID,
				ServerName: srv.Name,
				Status:     OAuthStatusFailed,
				Error:      tokResp.ErrorDescription,
			}, nil
		}
		return &OAuthState{
			ServerID:   srv.ID,
			ServerName: srv.Name,
			Status:     OAuthStatusFailed,
			Error:      tokResp.ErrorDescription,
		}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mcp: oauth token %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if tokResp.AccessToken == "" {
		return nil, fmt.Errorf("mcp: oauth token response missing access_token")
	}

	expiresAt := int64(0)
	if tokResp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tokResp.ExpiresIn) * time.Second).Unix()
	}
	tok := &oauthTokenSet{
		AccessToken:  tokResp.AccessToken,
		RefreshToken: tokResp.RefreshToken,
		TokenType:    tokResp.TokenType,
		ExpiresAt:    expiresAt,
	}
	if err := m.persistToken(ctx, srv, tok); err != nil {
		return nil, err
	}
	m.mu.Lock()
	delete(m.session, serverID)
	m.mu.Unlock()

	return &OAuthState{
		ServerID:   srv.ID,
		ServerName: srv.Name,
		Status:     OAuthStatusReady,
		ExpiresAt:  expiresAt,
	}, nil
}

// Token returns a valid access token for a server, refreshing from
// the persisted refresh_token when the access token is expired or
// missing.
func (m *OAuthManager) Token(ctx context.Context, serverID int64) (string, error) {
	srv, err := m.repo.GetMCPServer(ctx, serverID)
	if err != nil || srv == nil {
		return "", fmt.Errorf("mcp: server %d not found", serverID)
	}
	tok := m.decryptToken(srv.TokenJSON)
	if tok != nil && tok.AccessToken != "" {
		if tok.ExpiresAt == 0 || time.Now().Unix() < tok.ExpiresAt-60 {
			return tok.AccessToken, nil
		}
		// Expired: try refresh.
		if tok.RefreshToken != "" {
			nt, err := m.refresh(ctx, srv, tok)
			if err == nil {
				return nt.AccessToken, nil
			}
			logging.Warn("mcp oauth refresh failed", logging.F("server", srv.Name), logging.F("error", err.Error()))
		}
		return "", fmt.Errorf("mcp: oauth token expired for %s; re-authorize", srv.Name)
	}
	return "", fmt.Errorf("mcp: oauth not authorized for %s", srv.Name)
}

// refresh exchanges a refresh_token for a new access token.
func (m *OAuthManager) refresh(ctx context.Context, srv *store.MCPServer, tok *oauthTokenSet) (*oauthTokenSet, error) {
	cfg, err := m.parseConfig(srv)
	if err != nil {
		return nil, err
	}
	meta, err := m.discover(ctx, srv.URL)
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", tok.RefreshToken)
	form.Set("client_id", cfg.ClientID)
	if cfg.ClientSecret != "" {
		form.Set("client_secret", cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: oauth refresh request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mcp: oauth refresh %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tokResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokResp); err != nil {
		return nil, fmt.Errorf("mcp: oauth refresh decode: %w", err)
	}
	nt := &oauthTokenSet{AccessToken: tokResp.AccessToken, RefreshToken: tokResp.RefreshToken}
	if tokResp.RefreshToken == "" {
		nt.RefreshToken = tok.RefreshToken // keep old refresh token if not rotated
	}
	if tokResp.ExpiresIn > 0 {
		nt.ExpiresAt = time.Now().Add(time.Duration(tokResp.ExpiresIn) * time.Second).Unix()
	}
	if err := m.persistToken(ctx, srv, nt); err != nil {
		return nil, err
	}
	return nt, nil
}

// Clear removes any persisted token (for "re-authorize" from UI).
func (m *OAuthManager) Clear(ctx context.Context, serverID int64) error {
	srv, err := m.repo.GetMCPServer(ctx, serverID)
	if err != nil || srv == nil {
		return fmt.Errorf("mcp: server %d not found", serverID)
	}
	srv.TokenJSON = ""
	m.mu.Lock()
	delete(m.session, serverID)
	m.mu.Unlock()
	return m.repo.UpdateMCPServer(ctx, srv)
}

// discover fetches the RFC 8414 metadata document. Both the plain
// and .mcp suffixed well-known paths are tried.
func (m *OAuthManager) discover(ctx context.Context, baseURL string) (*oauthServerMetadata, error) {
	base := strings.TrimSuffix(baseURL, "/")
	paths := []string{
		base + "/.well-known/oauth-authorization-server.mcp",
		base + "/.well-known/oauth-authorization-server",
	}
	var lastErr error
	for _, p := range paths {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := m.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("mcp: oauth discover %d", resp.StatusCode)
			continue
		}
		var meta oauthServerMetadata
		if err := json.Unmarshal(body, &meta); err != nil {
			lastErr = fmt.Errorf("mcp: oauth discover decode: %w", err)
			continue
		}
		if meta.TokenEndpoint != "" {
			return &meta, nil
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("mcp: oauth discovery failed for %s", baseURL)
}

// register performs RFC 7591 dynamic client registration.
func (m *OAuthManager) register(ctx context.Context, srv *store.MCPServer, meta *oauthServerMetadata, cfg *oauthConfig) error {
	payload := map[string]any{
		"client_name":   "llmrx",
		"redirect_uris": []string{},
		"grant_types":   []string{"urn:ietf:params:oauth:grant-type:device_code", "refresh_token"},
	}
	if len(cfg.Scopes) > 0 {
		payload["scope"] = strings.Join(cfg.Scopes, " ")
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.RegistrationEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("mcp: oauth register request: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mcp: oauth register %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	var reg struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(rb, &reg); err != nil {
		return fmt.Errorf("mcp: oauth register decode: %w", err)
	}
	if reg.ClientID == "" {
		return fmt.Errorf("mcp: oauth registration returned no client_id")
	}
	cfg.ClientID = reg.ClientID
	if reg.ClientSecret != "" {
		cfg.ClientSecret = reg.ClientSecret
	}
	cfgJSON, _ := json.Marshal(cfg)
	srv.OAuthConfigJSON = string(cfgJSON)
	return m.repo.UpdateMCPServer(ctx, srv)
}

// parseConfig decodes the server's oauth_config_json.
func (m *OAuthManager) parseConfig(srv *store.MCPServer) (*oauthConfig, error) {
	cfg := &oauthConfig{}
	if srv.OAuthConfigJSON != "" && srv.OAuthConfigJSON != "{}" {
		if err := json.Unmarshal([]byte(srv.OAuthConfigJSON), cfg); err != nil {
			return nil, fmt.Errorf("mcp: invalid oauth_config_json for %s: %w", srv.Name, err)
		}
	}
	return cfg, nil
}

// encryptToken encrypts the token set with the secrets manager when
// available; falls back to plaintext JSON for dev setups.
func (m *OAuthManager) encryptToken(tok *oauthTokenSet) (string, error) {
	plain, err := json.Marshal(tok)
	if err != nil {
		return "", err
	}
	if m.secrets != nil {
		return m.secrets.Encrypt(plain)
	}
	return string(plain), nil
}

// decryptToken reverses encryptToken. Malformed or unreadable tokens
// return nil (caller treats as not authorized).
func (m *OAuthManager) decryptToken(tokenJSON string) *oauthTokenSet {
	if tokenJSON == "" {
		return nil
	}
	var raw []byte
	var err error
	if strings.HasPrefix(tokenJSON, "{") {
		raw = []byte(tokenJSON)
	} else {
		if m.secrets == nil {
			return nil
		}
		raw, err = m.secrets.Decrypt(tokenJSON)
		if err != nil {
			logging.Warn("mcp oauth token decrypt failed", logging.F("error", err.Error()))
			return nil
		}
	}
	var tok oauthTokenSet
	if err := json.Unmarshal(raw, &tok); err != nil {
		return nil
	}
	return &tok
}

// persistToken stores the token set on the server row.
func (m *OAuthManager) persistToken(ctx context.Context, srv *store.MCPServer, tok *oauthTokenSet) error {
	enc, err := m.encryptToken(tok)
	if err != nil {
		return err
	}
	srv.TokenJSON = enc
	return m.repo.UpdateMCPServer(ctx, srv)
}

// TokenProvider returns a tokenProvider func for a server, for
// injection into the HTTP transport. Returns nil when the server has
// no OAuth config.
func (m *OAuthManager) TokenProvider(serverID int64) func(ctx context.Context) (string, error) {
	if m == nil {
		return nil
	}
	return func(ctx context.Context) (string, error) {
		return m.Token(ctx, serverID)
	}
}

// parseDeviceCodeInterval is a small helper used by tests to keep
// interval parsing consistent.
func parseDeviceCodeInterval(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 5
	}
	return n
}
