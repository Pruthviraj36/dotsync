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
// Returns true only if both tokens were stored successfully.
func storeTokensInKeyring(accessToken, refreshToken string) bool {
	if accessToken != "" {
		if err := keyring.Set("dotsync", "access_token", accessToken); err != nil {
			return false
		}
	}
	if refreshToken != "" {
		if err := keyring.Set("dotsync", "refresh_token", refreshToken); err != nil {
			return false
		}
	}
	return true
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
	// Try environment variable override first (useful for CI/CD)
	envAccessToken := os.Getenv("DOTSYNC_ACCESS_TOKEN")
	if envAccessToken == "" {
		envAccessToken = os.Getenv("DOTSYNC_TOKEN")
	}

	// Try to load from config file
	path, err := globalConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		cfg := &GlobalConfig{ServerURL: ServerURL("")}
		if envAccessToken != "" {
			cfg.AccessToken = envAccessToken
		}
		return cfg, nil
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
	} else if cfg.AccessToken == "[stored in system keyring]" {
		cfg.AccessToken = ""
	}

	if refreshToken := getRefreshTokenFromKeyring(); refreshToken != "" {
		cfg.RefreshToken = refreshToken
	} else if cfg.RefreshToken == "[stored in system keyring]" {
		cfg.RefreshToken = ""
	}

	// Env token takes highest precedence
	if envAccessToken != "" {
		cfg.AccessToken = envAccessToken
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

	cfgForFile := *cfg

	// Store tokens in system keyring if available. If keyring fails (e.g. headless/no dbus),
	// keep tokens in the 0600 file so authentication is not lost.
	if storeTokensInKeyring(cfg.AccessToken, cfg.RefreshToken) {
		cfgForFile.AccessToken = "[stored in system keyring]"
		cfgForFile.RefreshToken = "[stored in system keyring]"
	}

	data, err := json.MarshalIndent(cfgForFile, "", "  ")
	if err != nil {
		return err
	}

	// Write-then-rename: an interrupted or concurrent save must never leave a
	// half-written config.json that forces the user to log in again.
	tmp, err := os.CreateTemp(filepath.Dir(path), "config.json.tmp-*")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close config: %w", err)
	}
	if err := os.Chmod(tmpName, 0600); err != nil {
		// Windows may not support chmod; CreateTemp already uses 0600.
		_ = err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// ClearGlobalIfStale removes stored credentials only when the refresh token on
// disk/keyring is still the one that was rejected. Concurrent dotsync processes
// (e.g. `dotsync ui` plus a CLI command) can rotate tokens independently — one
// of them failing must not delete the fresh credentials another just saved.
func ClearGlobalIfStale(usedRefreshToken string) error {
	current, err := LoadGlobal()
	if err == nil && current.RefreshToken != "" && current.RefreshToken != usedRefreshToken {
		return nil
	}
	return ClearGlobal()
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
	// Write-then-rename so a crash or full disk cannot leave a truncated
	// .dotsync.json (which would force the user to re-link the folder).
	tmp, err := os.CreateTemp(".", ".dotsync.json.tmp-*")
	if err != nil {
		return fmt.Errorf("create temp project config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write project config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close project config: %w", err)
	}
	if err := os.Chmod(tmpName, 0644); err != nil {
		// Windows may not support chmod; the temp file already has safe defaults.
		_ = err
	}
	if err := os.Rename(tmpName, projectFile); err != nil {
		return fmt.Errorf("replace project config: %w", err)
	}
	return nil
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
