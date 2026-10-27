package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Pruthviraj36/dotsync/cli/config"
)

const (
	// maxResponseBytes caps how much a server (or a MITM DNS-rebound host) can
	// make the CLI allocate for one response.
	maxResponseBytes = 16 << 20
	requestTimeout   = 30 * time.Second
	maxAttempts      = 3
)

// sharedTransport is process-wide: one connection pool, HTTP/2, keep-alives.
// Creating a fresh http.Client per call (as the device-flow helpers used to)
// throws away connections and adds a TLS handshake to every request.
var (
	sharedTransportOnce sync.Once
	sharedTransport     *http.Transport
)

func transport() *http.Transport {
	sharedTransportOnce.Do(func() {
		sharedTransport = &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ForceAttemptHTTP2:     true,
		}
	})
	return sharedTransport
}

func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: transport()}
}

type Client struct {
	baseURL    string
	httpClient *http.Client
	cfg        *config.GlobalConfig

	// refreshMu serialises refresh attempts. Refresh tokens rotate on every
	// use: two goroutines refreshing at once would race, and the loser would
	// look like a replayed token to the server.
	refreshMu sync.Mutex
}

func New(cfg *config.GlobalConfig) *Client {
	return &Client{
		baseURL:    cfg.ServerURL,
		httpClient: newHTTPClient(requestTimeout),
		cfg:        cfg,
	}
}

func validateProjectEnv(slug, env string) error {
	slug = strings.TrimSpace(slug)
	env = strings.TrimSpace(env)

	if slug == "" || strings.EqualFold(slug, "null") || strings.EqualFold(slug, "undefined") {
		return fmt.Errorf("project slug is missing — run: dotsync init")
	}

	if env == "" || strings.EqualFold(env, "null") || strings.EqualFold(env, "undefined") {
		return fmt.Errorf("environment is missing")
	}

	return nil
}

// tokens returns the current credentials under the same lock that guards
// refresh. The dashboard runs one Client across concurrent HTTP handlers, so
// reading these fields unlocked would race with a refresh in another goroutine
// (and a torn string read can crash or send a corrupt Authorization header).
func (c *Client) tokens() (accessToken, refreshToken string) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	return c.cfg.AccessToken, c.cfg.RefreshToken
}

// isServiceToken reports whether the current credential is a CI service token.
// Those are read-only and cannot be refreshed — spending a round trip on
// /api/auth/refresh just returns a confusing 401.
func (c *Client) isServiceToken() bool {
	accessToken, _ := c.tokens()
	return strings.HasPrefix(accessToken, "dst_")
}

func (c *Client) newRequest(method, path string, body []byte) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.baseURL+path, r)
	if err != nil {
		return nil, err
	}
	accessToken, _ := c.tokens()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dotsync-cli")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	return req, nil
}

// do executes an authenticated request with auto-refresh and bounded retries.
func (c *Client) do(method, path string, body any) (*http.Response, error) {
	var bodyBytes []byte
	var err error

	if body != nil {
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
	}

	idempotent := method == http.MethodGet || method == http.MethodHead

	for attempt := 0; ; attempt++ {
		req, reqErr := c.newRequest(method, path, bodyBytes)
		if reqErr != nil {
			return nil, reqErr
		}

		resp, doErr := c.httpClient.Do(req)
		if doErr != nil {
			// Only retry requests that are safe to repeat. Retrying a push on a
			// timeout could create two secret versions.
			if idempotent && attempt < maxAttempts-1 {
				time.Sleep(time.Duration(150*(1<<attempt)) * time.Millisecond)
				continue
			}
			return nil, fmt.Errorf("request failed: %w", doErr)
		}

		// 5xx from a proxy/load balancer is usually transient for reads.
		if idempotent && resp.StatusCode >= 500 && attempt < maxAttempts-1 {
			resp.Body.Close()
			time.Sleep(time.Duration(150*(1<<attempt)) * time.Millisecond)
			continue
		}

		if resp.StatusCode != http.StatusUnauthorized {
			return resp, nil
		}

		resp.Body.Close()

		// Service tokens and env-provided access tokens (CI) have no refresh
		// token. Retrying them would only mask the real problem.
		if c.isServiceToken() {
			return nil, fmt.Errorf("service token is invalid or expired")
		}
		_, refreshToken := c.tokens()
		if strings.TrimSpace(refreshToken) == "" {
			return nil, fmt.Errorf("session expired — run: dotsync login")
		}

		oldAccess, _ := c.tokens()
		c.refreshMu.Lock()
		// Another goroutine may have refreshed while we waited for the lock.
		if c.cfg.AccessToken == oldAccess {
			err = c.refreshTokensLocked()
		}
		c.refreshMu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("session expired and refresh failed:\n  %w\n\nplease log in again: dotsync login", err)
		}

		// Retry once with the new token (POST bodies were never processed, so
		// this cannot duplicate a write).
		if attempt >= maxAttempts-1 {
			return nil, fmt.Errorf("authentication failed after token refresh — run: dotsync login")
		}
	}
}

func (c *Client) refreshTokens() error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	return c.refreshTokensLocked()
}

// refreshTokensLocked must be called with refreshMu held.
func (c *Client) refreshTokensLocked() error {
	if strings.TrimSpace(c.cfg.RefreshToken) == "" {
		return fmt.Errorf("no refresh token — run: dotsync login")
	}
	// Remember which token we are exchanging: another dotsync process may rotate
	// the pair while this request is in flight.
	usedRefreshToken := c.cfg.RefreshToken
	requestBody, err := json.Marshal(map[string]string{"refresh_token": usedRefreshToken})
	if err != nil {
		return fmt.Errorf("marshal refresh request: %w", err)
	}
	resp, err := c.httpClient.Post(
		c.baseURL+"/api/auth/refresh",
		"application/json",
		bytes.NewReader(requestBody),
	)
	if err != nil {
		return fmt.Errorf("token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Before treating this as a dead session, check whether a concurrent
		// process (~dotsync ui~ plus a CLI command) already rotated the pair.
		// Its credentials are valid, so adopt them instead of destroying them.
		if fresh, loadErr := config.LoadGlobal(); loadErr == nil &&
			fresh.RefreshToken != "" && fresh.RefreshToken != usedRefreshToken {
			c.cfg.AccessToken = fresh.AccessToken
			c.cfg.RefreshToken = fresh.RefreshToken
			return nil
		}

		// Nothing newer on disk: the refresh token really is unusable. Clear it
		// so the next `dotsync login` starts a fresh device flow. The guarded
		// clear re-checks the file, so a save that lands during this call wins.
		_ = config.ClearGlobalIfStale(usedRefreshToken)
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(responseBody, &apiErr)
		if apiErr.Error != "" {
			return fmt.Errorf("refresh failed: %s", apiErr.Error)
		}
		return fmt.Errorf("refresh failed with status %d", resp.StatusCode)
	}

	// Parse and validate token response
	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return fmt.Errorf("parse refresh response: %w", err)
	}

	// Validate that tokens were actually provided
	if result.AccessToken == "" {
		return fmt.Errorf("refresh response missing access_token — server returned invalid response")
	}
	if result.RefreshToken == "" {
		return fmt.Errorf("refresh response missing refresh_token — server returned invalid response")
	}

	// Basic format validation (tokens should be non-empty strings)
	if len(result.AccessToken) < 10 || len(result.RefreshToken) < 10 {
		return fmt.Errorf("refresh response contains invalid token format")
	}

	// Update config with new tokens
	c.cfg.AccessToken = result.AccessToken
	c.cfg.RefreshToken = result.RefreshToken
	if err := config.SaveGlobal(c.cfg); err != nil {
		return fmt.Errorf("save refreshed tokens: %w", err)
	}

	return nil
}

// decodeResponse reads JSON body and checks for error field.
func decodeResponse(resp *http.Response, target any) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &apiErr)
		if apiErr.Error != "" {
			return fmt.Errorf("server error: %s", apiErr.Error)
		}
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}

	if target != nil {
		if len(bytes.TrimSpace(body)) == 0 {
			return nil
		}
		if err := json.Unmarshal(body, target); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}
	}
	return nil
}

// ── API methods ──────────────────────────────────────────────────────────────

func (c *Client) GetMe() (map[string]any, error) {
	resp, err := c.do("GET", "/api/auth/me", nil)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := decodeResponse(resp, &result); err != nil {
		return nil, err
	}
	return result, nil
}

type PushRequest struct {
	EncryptedData []byte `json:"encrypted_data"`
	Nonce         []byte `json:"nonce"`
	Signature     []byte `json:"signature,omitempty"` // ed25519 sig over sha256(encrypted_data)
	SignerPubKey  string `json:"signer_pubkey,omitempty"`
}

type PushResponse struct {
	Version   int    `json:"version"`
	CreatedAt string `json:"created_at"`
}

func (c *Client) Push(slug, env string, req PushRequest) (*PushResponse, error) {
	if err := validateProjectEnv(slug, env); err != nil {
		return nil, err
	}

	path := fmt.Sprintf(
		"/api/projects/%s/envs/%s/push",
		url.PathEscape(slug),
		url.PathEscape(env),
	)

	resp, err := c.do("POST", path, req)
	if err != nil {
		return nil, err
	}

	var result PushResponse
	return &result, decodeResponse(resp, &result)
}

type PullResponse struct {
	EncryptedData  []byte `json:"encrypted_data"`
	Nonce          []byte `json:"nonce"`
	Signature      []byte `json:"signature,omitempty"`
	Version        int    `json:"version"`
	PushedBy       string `json:"pushed_by"` // username, resolved server-side
	PushedByPubKey string `json:"pushed_by_pubkey,omitempty"`
	CreatedAt      string `json:"created_at"`
}

func (c *Client) Pull(slug, env string) (*PullResponse, error) {
	if err := validateProjectEnv(slug, env); err != nil {
		return nil, err
	}

	path := fmt.Sprintf(
		"/api/projects/%s/envs/%s/pull",
		url.PathEscape(slug),
		url.PathEscape(env),
	)

	resp, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}

	var result PullResponse
	return &result, decodeResponse(resp, &result)
}

// SetPubKey uploads this machine's ed25519 public key so teammates can
// verify signatures on anything this account pushes.
func (c *Client) SetPubKey(pubkeyHex string) error {
	resp, err := c.do("PUT", "/api/me/pubkey", map[string]string{"pubkey": pubkeyHex})
	if err != nil {
		return err
	}
	return decodeResponse(resp, nil)
}

type HistoryEntry struct {
	Version   int    `json:"version"`
	PushedBy  string `json:"pushed_by"`
	CreatedAt string `json:"created_at"`
}

func (c *Client) History(slug, env string) ([]HistoryEntry, error) {
	if err := validateProjectEnv(slug, env); err != nil {
		return nil, err
	}
	path := fmt.Sprintf(
		"/api/projects/%s/envs/%s/history",
		url.PathEscape(slug),
		url.PathEscape(env),
	)
	resp, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	var result []HistoryEntry
	if err := decodeResponse(resp, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) CreateProject(name, slug, description string) (map[string]any, error) {
	resp, err := c.do("POST", "/api/projects", map[string]string{
		"name": name, "slug": slug, "description": description,
	})
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := decodeResponse(resp, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) ListProjects() ([]map[string]any, error) {
	resp, err := c.do("GET", "/api/projects", nil)
	if err != nil {
		return nil, err
	}
	var result []map[string]any
	if err := decodeResponse(resp, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// SetProjectPassword stores/rotates the E2EE project password on the server.
// Requires owner or admin role on the project.
func (c *Client) SetProjectPassword(slug, password string) error {
	resp, err := c.do("PUT", fmt.Sprintf("/api/projects/%s/password", url.PathEscape(slug)), map[string]string{
		"password": password,
	})
	if err != nil {
		return err
	}
	return decodeResponse(resp, nil)
}

// GetProjectPassword fetches the E2EE project password from the server.
// Any project member can call this.
func (c *Client) GetProjectPassword(slug string) (string, error) {
	resp, err := c.do("GET", fmt.Sprintf("/api/projects/%s/password", url.PathEscape(slug)), nil)
	if err != nil {
		return "", err
	}
	var result struct {
		Password string `json:"password"`
	}
	if err := decodeResponse(resp, &result); err != nil {
		return "", err
	}
	return result.Password, nil
}

func (c *Client) AddTeamMember(slug, username, role string) error {
	resp, err := c.do("POST", fmt.Sprintf("/api/projects/%s/team", url.PathEscape(slug)), map[string]string{
		"username": username,
		"role":     role,
	})
	if err != nil {
		return err
	}
	return decodeResponse(resp, nil)
}

func (c *Client) Logout() error {
	resp, err := c.do("POST", "/api/auth/logout", map[string]string{})
	if err != nil {
		return err
	}
	return decodeResponse(resp, nil)
}

// AuthConfig is the public OAuth config served by /api/auth/config.
type AuthConfig struct {
	GitHubClientID string `json:"github_client_id"`
}

// GetAuthConfig fetches the server's public OAuth client ID.
// No authentication required — this is intentionally a public endpoint
// since GitHub client IDs are not secrets.
func GetAuthConfig(serverURL string) (*AuthConfig, error) {
	client := newHTTPClient(15 * time.Second)
	resp, err := client.Get(serverURL + "/api/auth/config")
	if err != nil {
		return nil, fmt.Errorf("connect to server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var cfg AuthConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	return &cfg, nil
}

// ── GitHub OAuth Device Flow ────────────────────────────────────────────
//
// This talks directly to GitHub, not to the DotSync server. GitHub designed
// the device flow specifically so public clients (like this CLI) never need
// to hold a client secret — see https://docs.github.com/en/apps/oauth-apps/
// building-oauth-apps/authorizing-oauth-apps#device-flow. The only thing the
// DotSync server needs from us afterward is the resulting GitHub access
// token, which it independently verifies against GitHub's own /user API
// before issuing DotSync credentials (see GitHubDeviceLogin server-side —
// the server never just trusts whatever token the CLI hands it).
//
// This also means login now works identically whether the CLI is running
// on the same machine as the browser, over SSH on a remote box, or inside
// a container — the user can complete the browser step on literally any
// device with internet access, since all that's needed is typing a short
// code at github.com/login/device.

const githubDeviceCodeURL = "https://github.com/login/device/code"
const githubTokenURL = "https://github.com/login/oauth/access_token"

// DeviceCodeResponse is GitHub's response to a device code request.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// StartGitHubDeviceFlow requests a device code + user code from GitHub.
// The CLI shows the user code and verification URI to the user, who enters
// the code at that URL on any device with a browser.
func StartGitHubDeviceFlow(clientID string) (*DeviceCodeResponse, error) {
	client := newHTTPClient(15 * time.Second)
	form := url.Values{
		"client_id": {clientID},
		"scope":     {"read:user user:email"},
	}

	req, err := http.NewRequest("POST", githubDeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect to github: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusBadRequest {
		return nil, fmt.Errorf("github rejected the device code request — " +
			"is Device Flow enabled on the GitHub OAuth App? " +
			"(github.com/settings/developers → your app → Advanced)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github returned %d", resp.StatusCode)
	}

	var dc DeviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&dc); err != nil {
		return nil, fmt.Errorf("decode device code response: %w", err)
	}
	if dc.DeviceCode == "" || dc.UserCode == "" {
		return nil, fmt.Errorf("github returned an incomplete device code response")
	}
	return &dc, nil
}

// PollErrAuthorizationPending is returned while the user hasn't yet
// completed the browser step — the caller should keep polling.
var PollErrAuthorizationPending = fmt.Errorf("authorization_pending")

// PollErrExpired means the user_code expired (15 minutes) before the user
// completed authorization — the caller must restart the whole flow.
var PollErrExpired = fmt.Errorf("expired_token")

// PollErrAccessDenied means the user explicitly declined authorization.
var PollErrAccessDenied = fmt.Errorf("access_denied")

// PollErrSlowDown means GitHub is asking us to poll less frequently —
// the caller must add 5 seconds to its polling interval, cumulatively,
// per RFC 8628 section 3.5.
var PollErrSlowDown = fmt.Errorf("slow_down")

// PollGitHubDeviceToken makes a single poll request to GitHub's token
// endpoint. Returns PollErrAuthorizationPending if the user hasn't finished
// yet (the normal case while waiting) — callers should sleep for `interval`
// seconds and call this again.
func PollGitHubDeviceToken(clientID, deviceCode string) (string, error) {
	client := newHTTPClient(15 * time.Second)
	form := url.Values{
		"client_id":   {clientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}

	req, err := http.NewRequest("POST", githubTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("connect to github: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}

	switch result.Error {
	case "":
		if result.AccessToken == "" {
			return "", fmt.Errorf("github returned no access token and no error")
		}
		return result.AccessToken, nil
	case "authorization_pending":
		return "", PollErrAuthorizationPending
	case "slow_down":
		return "", PollErrSlowDown
	case "expired_token":
		return "", PollErrExpired
	case "access_denied":
		return "", PollErrAccessDenied
	default:
		return "", fmt.Errorf("github device auth error: %s", result.Error)
	}
}

// ExchangeGitHubDeviceToken sends the verified GitHub access token to the
// DotSync server, which independently re-verifies it against GitHub's own
// /user API and, if valid, issues DotSync access + refresh tokens.
func ExchangeGitHubDeviceToken(serverURL, githubAccessToken string) (*LoginResponse, error) {
	client := newHTTPClient(15 * time.Second)
	body, _ := json.Marshal(map[string]string{"github_access_token": githubAccessToken})

	resp, err := client.Post(serverURL+"/api/auth/github/device", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("connect to server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		if apiErr.Error != "" {
			return nil, fmt.Errorf("server rejected token: %s", apiErr.Error)
		}
		return nil, fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var result LoginResponse
	return &result, json.NewDecoder(resp.Body).Decode(&result)
}

type LoginResponse struct {
	AccessToken  string         `json:"access_token"`
	RefreshToken string         `json:"refresh_token"`
	User         map[string]any `json:"user"`
}

// ListTeamMembers returns all members of a project with their roles.
func (c *Client) ListTeamMembers(slug string) ([]map[string]any, error) {
	resp, err := c.do("GET", fmt.Sprintf("/api/projects/%s/team", url.PathEscape(slug)), nil)
	if err != nil {
		return nil, err
	}
	var result []map[string]any
	if err := decodeResponse(resp, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// RemoveTeamMember removes a user from the project team.
func (c *Client) RemoveTeamMember(slug, username string) error {
	resp, err := c.do("DELETE", fmt.Sprintf("/api/projects/%s/team/%s", url.PathEscape(slug), url.PathEscape(username)), nil)
	if err != nil {
		return err
	}
	return decodeResponse(resp, nil)
}

// UpdateTeamRole changes a member's role.
func (c *Client) UpdateTeamRole(slug, username, role string) error {
	resp, err := c.do("PATCH", fmt.Sprintf("/api/projects/%s/team/%s", url.PathEscape(slug), url.PathEscape(username)),
		map[string]string{"role": role})
	if err != nil {
		return err
	}
	return decodeResponse(resp, nil)
}

// PullVersion fetches a specific version of secrets.
func (c *Client) PullVersion(slug, env string, version int) (*PullResponse, error) {
	if err := validateProjectEnv(slug, env); err != nil {
		return nil, err
	}
	path := fmt.Sprintf(
		"/api/projects/%s/envs/%s/pull/version?version=%d",
		url.PathEscape(slug),
		url.PathEscape(env),
		version,
	)
	resp, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	var result PullResponse
	return &result, decodeResponse(resp, &result)
}

// AuditLogs fetches the audit log for a project.
func (c *Client) AuditLogs(slug string) ([]map[string]any, error) {
	resp, err := c.do("GET", fmt.Sprintf("/api/projects/%s/audit", url.PathEscape(slug)), nil)
	if err != nil {
		return nil, err
	}
	var result []map[string]any
	if err := decodeResponse(resp, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetLatestVersion fetches just the latest version number for sync-state comparison.
func (c *Client) GetLatestVersion(slug, env string) (int, string, error) {
	if err := validateProjectEnv(slug, env); err != nil {
		return 0, "", err
	}
	path := fmt.Sprintf(
		"/api/projects/%s/envs/%s/head",
		url.PathEscape(slug),
		url.PathEscape(env),
	)
	resp, err := c.do("GET", path, nil)
	if err != nil {
		return 0, "", err
	}
	var head struct {
		Version  int    `json:"version"`
		PushedBy string `json:"pushed_by"`
	}
	if err := decodeResponse(resp, &head); err != nil {
		return 0, "", err
	}
	return head.Version, head.PushedBy, nil
}

// ── Service Token API methods ────────────────────────────────────────────────

// CreateServiceToken creates a scoped CI/CD service token for a project+env.
// Returns the raw token (shown once — store it immediately as a secret).
func (c *Client) CreateServiceToken(projectSlug, env, name string) (string, error) {
	resp, err := c.do("POST", fmt.Sprintf("/api/projects/%s/tokens", url.PathEscape(projectSlug)), map[string]string{
		"env":  env,
		"name": name,
	})
	if err != nil {
		return "", err
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := decodeResponse(resp, &result); err != nil {
		return "", err
	}
	if result.Token == "" {
		return "", fmt.Errorf("server returned empty token")
	}
	return result.Token, nil
}

// ListServiceTokens lists all service tokens for a project.
func (c *Client) ListServiceTokens(projectSlug string) ([]map[string]any, error) {
	resp, err := c.do("GET", fmt.Sprintf("/api/projects/%s/tokens", url.PathEscape(projectSlug)), nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Tokens []map[string]any `json:"tokens"`
	}
	if err := decodeResponse(resp, &result); err != nil {
		return nil, err
	}
	return result.Tokens, nil
}

// RevokeServiceToken revokes a service token by ID.
func (c *Client) RevokeServiceToken(projectSlug, tokenID string) error {
	resp, err := c.do("DELETE", fmt.Sprintf("/api/projects/%s/tokens/%s", url.PathEscape(projectSlug), url.PathEscape(tokenID)), nil)
	if err != nil {
		return err
	}
	return decodeResponse(resp, nil)
}

// ListEnvironments returns environment names for a project (e.g. ["dev","staging","production"]).
func (c *Client) ListEnvironments(projectSlug string) ([]string, error) {
	resp, err := c.do("GET", fmt.Sprintf("/api/projects/%s/envs", url.PathEscape(projectSlug)), nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Environments []struct {
			Name string `json:"name"`
		} `json:"environments"`
	}
	if err := decodeResponse(resp, &result); err != nil {
		return nil, err
	}
	names := make([]string, len(result.Environments))
	for i, e := range result.Environments {
		names[i] = e.Name
	}
	return names, nil
}
