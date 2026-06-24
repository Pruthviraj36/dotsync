package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Pruthviraj36/dotsync/cli/config"
	"github.com/Pruthviraj36/dotsync/internal/crypto"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
	cfg        *config.GlobalConfig
}

func New(cfg *config.GlobalConfig) *Client {
	return &Client{
		baseURL: cfg.ServerURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		cfg: cfg,
	}
}

// do executes an authenticated request with HMAC signing and auto-refresh.
//
// Token handling: if the stored access token is already expired (or within
// 30s of expiry), we proactively refresh before sending — this avoids the
// failure case where Argon2id key derivation takes long enough that a token
// that was valid at the start of push() is expired by the time the HTTP
// request fires.
//
// Retry: on a 401 we refresh and rebuild a brand-new *http.Request rather
// than mutating the original — the original request's Body is already fully
// consumed by the first Do() call and cannot be re-read.
func (c *Client) do(method, path string, body any) (*http.Response, error) {
	var bodyBytes []byte
	var err error

	if body != nil {
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
	}

	// Proactively refresh if token is expired or expiring within 30s.
	// This covers the case where Argon2id key derivation (64 MB, 3 passes)
	// runs long enough to push us past the 15-minute JWT TTL.
	if c.tokenExpired() {
		if err := c.refreshTokens(); err != nil {
			return nil, fmt.Errorf("session expired — run: dotsync login")
		}
	}

	resp, err := c.buildAndSend(method, path, bodyBytes)
	if err != nil {
		return nil, err
	}

	// If still 401 after proactive refresh, try one reactive refresh + retry.
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if err := c.refreshTokens(); err != nil {
			return nil, fmt.Errorf("session expired — run: dotsync login")
		}
		// Build a completely fresh request — the original Body is consumed.
		resp, err = c.buildAndSend(method, path, bodyBytes)
		if err != nil {
			return nil, err
		}
	}

	return resp, nil
}

// buildAndSend constructs a signed HTTP request and executes it.
// Always builds a fresh request so the Body reader is never exhausted.
func (c *Client) buildAndSend(method, path string, bodyBytes []byte) (*http.Response, error) {
	req, err := http.NewRequest(method, c.baseURL+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)

	// HMAC-sign the request body using access token as HMAC secret.
	// GET requests sign an empty payload so the header is always present.
	sigPayload := bodyBytes
	if len(sigPayload) == 0 {
		sigPayload = []byte("")
	}
	req.Header.Set("X-DotSync-Signature", crypto.HMACSign([]byte(c.cfg.AccessToken), sigPayload))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	return resp, nil
}

// tokenExpired reports whether the stored access token is expired or will
// expire within the next 30 seconds. Parses the JWT claims without
// verifying the signature (the server will verify — we just want the exp).
func (c *Client) tokenExpired() bool {
	if c.cfg.AccessToken == "" {
		return true
	}
	parts := strings.Split(c.cfg.AccessToken, ".")
	if len(parts) != 3 {
		return true
	}
	// JWT payload is base64url-encoded without padding — add it back
	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return true
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return true
	}
	// Treat as expired if within 30 seconds of actual expiry
	return time.Now().Add(30 * time.Second).Unix() >= claims.Exp
}

func (c *Client) refreshTokens() error {
	resp, err := c.httpClient.Post(
		c.baseURL+"/api/auth/refresh",
		"application/json",
		bytes.NewBufferString(`{"refresh_token":"`+c.cfg.RefreshToken+`"}`),
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("refresh failed")
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	c.cfg.AccessToken = result.AccessToken
	c.cfg.RefreshToken = result.RefreshToken
	return config.SaveGlobal(c.cfg)
}

// decodeResponse reads JSON body and checks for error field.
func decodeResponse(resp *http.Response, target any) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
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
		return json.Unmarshal(body, target)
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
	return result, decodeResponse(resp, &result)
}

type PushRequest struct {
	EncryptedData []byte `json:"encrypted_data"`
	Nonce         []byte `json:"nonce"`
}

type PushResponse struct {
	Version   int    `json:"version"`
	CreatedAt string `json:"created_at"`
}

func (c *Client) Push(slug, env string, req PushRequest) (*PushResponse, error) {
	resp, err := c.do("POST", fmt.Sprintf("/api/projects/%s/envs/%s/push", slug, env), req)
	if err != nil {
		return nil, err
	}
	var result PushResponse
	return &result, decodeResponse(resp, &result)
}

type PullResponse struct {
	EncryptedData []byte `json:"encrypted_data"`
	Nonce         []byte `json:"nonce"`
	Version       int    `json:"version"`
	PushedBy      string `json:"pushed_by"`
	CreatedAt     string `json:"created_at"`
}

func (c *Client) Pull(slug, env string) (*PullResponse, error) {
	resp, err := c.do("GET", fmt.Sprintf("/api/projects/%s/envs/%s/pull", slug, env), nil)
	if err != nil {
		return nil, err
	}
	var result PullResponse
	return &result, decodeResponse(resp, &result)
}

type HistoryEntry struct {
	Version   int    `json:"version"`
	PushedBy  string `json:"pushed_by"`
	CreatedAt string `json:"created_at"`
}

func (c *Client) History(slug, env string) ([]HistoryEntry, error) {
	resp, err := c.do("GET", fmt.Sprintf("/api/projects/%s/envs/%s/history", slug, env), nil)
	if err != nil {
		return nil, err
	}
	var result []HistoryEntry
	return result, decodeResponse(resp, &result)
}

func (c *Client) CreateProject(name, slug, description string) (map[string]any, error) {
	resp, err := c.do("POST", "/api/projects", map[string]string{
		"name": name, "slug": slug, "description": description,
	})
	if err != nil {
		return nil, err
	}
	var result map[string]any
	return result, decodeResponse(resp, &result)
}

func (c *Client) ListProjects() ([]map[string]any, error) {
	resp, err := c.do("GET", "/api/projects", nil)
	if err != nil {
		return nil, err
	}
	var result []map[string]any
	return result, decodeResponse(resp, &result)
}

func (c *Client) AddTeamMember(slug, username string) error {
	resp, err := c.do("POST", fmt.Sprintf("/api/projects/%s/team", slug), map[string]string{
		"username": username,
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
	client := &http.Client{Timeout: 15 * time.Second}
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
func StartGitHubDeviceFlow(clientID string) (*DeviceCodeResponse, error) {
	client := &http.Client{Timeout: 15 * time.Second}
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

// PollErrAuthorizationPending is returned while the user hasn't yet completed the browser step.
var PollErrAuthorizationPending = fmt.Errorf("authorization_pending")

// PollErrExpired means the user_code expired before the user completed authorization.
var PollErrExpired = fmt.Errorf("expired_token")

// PollErrAccessDenied means the user explicitly declined authorization.
var PollErrAccessDenied = fmt.Errorf("access_denied")

// PollErrSlowDown means GitHub is asking us to poll less frequently.
var PollErrSlowDown = fmt.Errorf("slow_down")

// PollGitHubDeviceToken makes a single poll request to GitHub's token endpoint.
func PollGitHubDeviceToken(clientID, deviceCode string) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
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

// ExchangeGitHubDeviceToken sends the verified GitHub access token to the DotSync server.
func ExchangeGitHubDeviceToken(serverURL, githubAccessToken string) (*LoginResponse, error) {
	client := &http.Client{Timeout: 15 * time.Second}
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
	resp, err := c.do("GET", fmt.Sprintf("/api/projects/%s/team", slug), nil)
	if err != nil {
		return nil, err
	}
	var result []map[string]any
	return result, decodeResponse(resp, &result)
}

// RemoveTeamMember removes a user from the project team.
func (c *Client) RemoveTeamMember(slug, username string) error {
	resp, err := c.do("DELETE", fmt.Sprintf("/api/projects/%s/team/%s", slug, username), nil)
	if err != nil {
		return err
	}
	return decodeResponse(resp, nil)
}

// UpdateTeamRole changes a member's role.
func (c *Client) UpdateTeamRole(slug, username, role string) error {
	resp, err := c.do("PATCH", fmt.Sprintf("/api/projects/%s/team/%s", slug, username),
		map[string]string{"role": role})
	if err != nil {
		return err
	}
	return decodeResponse(resp, nil)
}

// PullVersion fetches a specific version of secrets.
func (c *Client) PullVersion(slug, env string, version int) (*PullResponse, error) {
	path := fmt.Sprintf("/api/projects/%s/envs/%s/pull/version?version=%d", slug, env, version)
	resp, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	var result PullResponse
	return &result, decodeResponse(resp, &result)
}

// AuditLogs fetches the audit log for a project.
func (c *Client) AuditLogs(slug string) ([]map[string]any, error) {
	resp, err := c.do("GET", fmt.Sprintf("/api/projects/%s/audit", slug), nil)
	if err != nil {
		return nil, err
	}
	var result []map[string]any
	return result, decodeResponse(resp, &result)
}

// GetLatestVersion fetches just the latest version number for sync-state comparison.
func (c *Client) GetLatestVersion(slug, env string) (int, string, error) {
	path := fmt.Sprintf("/api/projects/%s/envs/%s/history", slug, env)
	resp, err := c.do("GET", path, nil)
	if err != nil {
		return 0, "", err
	}
	var history []HistoryEntry
	if err := decodeResponse(resp, &history); err != nil {
		return 0, "", err
	}
	if len(history) == 0 {
		return 0, "", nil
	}
	return history[0].Version, history[0].PushedBy, nil
}
