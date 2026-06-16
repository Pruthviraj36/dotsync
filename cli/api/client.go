package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/yourusername/dotsync/cli/config"
	"github.com/yourusername/dotsync/internal/crypto"
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
func (c *Client) do(method, path string, body any) (*http.Response, error) {
	var bodyBytes []byte
	var err error

	if body != nil {
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
	}

	req, err := http.NewRequest(method, c.baseURL+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)

	// HMAC-sign the request body using access token as HMAC secret
	// This proves the request came from a holder of a valid token
	if len(bodyBytes) > 0 {
		sig := crypto.HMACSign([]byte(c.cfg.AccessToken), bodyBytes)
		req.Header.Set("X-DotSync-Signature", sig)
	} else {
		// Sign an empty body marker for GET requests
		sig := crypto.HMACSign([]byte(c.cfg.AccessToken), []byte(""))
		req.Header.Set("X-DotSync-Signature", sig)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	// If 401, try to refresh and retry once
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if err := c.refreshTokens(); err != nil {
			return nil, fmt.Errorf("session expired — run: dotsync login")
		}

		// Retry with new token
		req.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)
		if len(bodyBytes) > 0 {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			sig := crypto.HMACSign([]byte(c.cfg.AccessToken), bodyBytes)
			req.Header.Set("X-DotSync-Signature", sig)
		}
		resp, err = c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
	}

	return resp, nil
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

func (c *Client) Logout() error {
	resp, err := c.do("POST", "/api/auth/logout", map[string]string{})
	if err != nil {
		return err
	}
	return decodeResponse(resp, nil)
}

// ExchangeGitHubCode exchanges an OAuth code for DotSync tokens.
func ExchangeGitHubCode(serverURL, code string) (*LoginResponse, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	body, _ := json.Marshal(map[string]string{"code": code})

	resp, err := client.Post(serverURL+"/api/auth/github", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("connect to server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var apiErr struct{ Error string `json:"error"` }
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		return nil, fmt.Errorf("auth failed: %s", apiErr.Error)
	}

	var result LoginResponse
	return &result, json.NewDecoder(resp.Body).Decode(&result)
}

type LoginResponse struct {
	AccessToken  string         `json:"access_token"`
	RefreshToken string         `json:"refresh_token"`
	User         map[string]any `json:"user"`
}
