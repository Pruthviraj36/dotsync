package config

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	configDirName = ".dotsync"
	globalFile    = "config.json"
	projectFile   = ".dotsync.json"
)

// GlobalConfig stores credentials in ~/.dotsync/config.json
type GlobalConfig struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	UserID       string `json:"user_id"`
	Username     string `json:"username"`
	ServerURL    string `json:"server_url"`
}

// ProjectConfig stores project binding in .dotsync.json (project root)
type ProjectConfig struct {
	ProjectSlug      string                 `json:"project_slug"`
	DefaultEnv       string                 `json:"default_env"`
	LastPulledStates map[string]PulledState `json:"last_pulled_states,omitempty"`
}

// PulledState records the exact local file state produced by a successful pull.
// The digest lets callers avoid a download without hiding edits made manually.
type PulledState struct {
	Version int    `json:"version"`
	Digest  string `json:"digest"`
}

// ── Global config ─────────────────────────────────────────────────────────────

func globalConfigPath() (string, error) {
	// DOTSYNC_CONFIG_DIR lets you explicitly override the config directory.
	// Useful when running under sudo where HOME points to /root.
	if dir := os.Getenv("DOTSYNC_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, globalFile), nil
	}
	// Under `sudo`, HOME is often /root. Use SUDO_USER's home if available
	// so `sudo dotsync run` uses the same config as `dotsync`.
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		if home, err := userHomeDir(sudoUser); err == nil {
			return filepath.Join(home, configDirName, globalFile), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, configDirName, globalFile), nil
}

// userHomeDir returns the home directory for a specific username.
// Works cross-platform: Linux, macOS, Windows/WSL.
func userHomeDir(username string) (string, error) {
	// Use os/user package for proper cross-platform home directory lookup
	u, err := user.Lookup(username)
	if err != nil {
		// If lookup fails, try environment variable as fallback
		if home := os.Getenv("HOME"); home != "" {
			return home, nil
		}
		return "", fmt.Errorf("could not look up user %q: %w (and HOME not set)", username, err)
	}
	if u.HomeDir == "" {
		return "", fmt.Errorf("user %q has no home directory", username)
	}
	return u.HomeDir, nil
}

// ── Credential Storage (System Keyring) ─────────────────────────────────────

// storeTokensInKeyring attempts to store tokens securely in system keyring.
// Fails silently if keyring is not available (e.g., no desktop environment).
func storeTokensInKeyring(accessToken, refreshToken string) {
	// Store access token
	if accessToken != "" {
		_ = keyring.Set("dotsync", "access_token", accessToken)
	}
	// Store refresh token
	if refreshToken != "" {
		_ = keyring.Set("dotsync", "refresh_token", refreshToken)
	}
}

// getAccessTokenFromKeyring attempts to retrieve access token from system keyring.
// Returns empty string if not found or keyring unavailable.
func getAccessTokenFromKeyring() string {
	token, err := keyring.Get("dotsync", "access_token")
	if err != nil {
		return ""
	}
	return token
}

// getRefreshTokenFromKeyring attempts to retrieve refresh token from system keyring.
// Returns empty string if not found or keyring unavailable.
func getRefreshTokenFromKeyring() string {
	token, err := keyring.Get("dotsync", "refresh_token")
	if err != nil {
		return ""
	}
	return token
}

// clearTokensFromKeyring removes tokens from system keyring.
// Used during logout.
func clearTokensFromKeyring() {
	_ = keyring.Delete("dotsync", "access_token")
	_ = keyring.Delete("dotsync", "refresh_token")
}

func LoadGlobal() (*GlobalConfig, error) {
	// Try to load from config file
	path, err := globalConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &GlobalConfig{ServerURL: ServerURL("")}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg GlobalConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Try to get tokens from system keyring (more secure than plaintext file)
	if accessToken := getAccessTokenFromKeyring(); accessToken != "" {
		cfg.AccessToken = accessToken
	}
	if refreshToken := getRefreshTokenFromKeyring(); refreshToken != "" {
		cfg.RefreshToken = refreshToken
	}

	// Always let DOTSYNC_SERVER env var override the saved value.
	// This means switching servers never requires editing config.json.
	cfg.ServerURL = ServerURL(cfg.ServerURL)
	return &cfg, nil
}

func SaveGlobal(cfg *GlobalConfig) error {
	path, err := globalConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// Store tokens in system keyring (more secure than plaintext JSON)
	storeTokensInKeyring(cfg.AccessToken, cfg.RefreshToken)

	// Create a copy for file storage without plaintext tokens
	cfgForFile := *cfg
	cfgForFile.AccessToken = "[stored in system keyring]"
	cfgForFile.RefreshToken = "[stored in system keyring]"

	data, err := json.MarshalIndent(cfgForFile, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func ClearGlobal() error {
	path, err := globalConfigPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	// SaveGlobal stores credentials in the OS keyring. Removing only the JSON
	// file would leave stale tokens behind, making logout followed by login
	// appear to do nothing after a rotated/revoked refresh token.
	clearTokensFromKeyring()
	return nil
}

// ── Project config ────────────────────────────────────────────────────────────

func LoadProject() (*ProjectConfig, error) {
	data, err := os.ReadFile(projectFile)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("not a dotsync project — run: dotsync init")
	}
	if err != nil {
		return nil, fmt.Errorf("read project config: %w", err)
	}
	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse project config: %w", err)
	}
	if cfg.LastPulledStates == nil {
		cfg.LastPulledStates = make(map[string]PulledState)
	}
	return &cfg, nil
}

func SaveProject(cfg *ProjectConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(projectFile, data, 0644)
}

// ── Server URL resolution ─────────────────────────────────────────────────────
//
// Priority (highest to lowest):
//   1. DOTSYNC_SERVER env var  — always wins, set this in CI or to switch servers
//   2. saved value in config.json  — set by `dotsync config set-server <url>`
//   3. nothing → error at login time (no hidden default; must be explicit)
//
// To switch servers you never touch source code — just set DOTSYNC_SERVER:
//   export DOTSYNC_SERVER=https://your-server.example.com

// ServerURL resolves the active server URL from env or saved config.
// saved is the value from config.json (may be empty on first run).
func ServerURL(saved string) string {
	if env := os.Getenv("DOTSYNC_SERVER"); env != "" {
		return strings.TrimRight(env, "/")
	}
	if saved != "" {
		return strings.TrimRight(saved, "/")
	}
	// No default — callers that need a URL must check IsServerConfigured.
	return ""
}

// IsServerConfigured returns true if a server URL is available.
func IsServerConfigured(cfg *GlobalConfig) bool {
	return cfg != nil && cfg.ServerURL != ""
}

// IsLoggedIn checks if there's a valid stored token.
func IsLoggedIn(cfg *GlobalConfig) bool {
	return cfg != nil && cfg.AccessToken != ""
}

// DefaultServerURL is kept for backwards compat with any callers that
// reference it — it delegates to ServerURL with no saved value.
func DefaultServerURL() string {
	return ServerURL("")
}
