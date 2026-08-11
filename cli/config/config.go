package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	ProjectSlug string `json:"project_slug"`
	DefaultEnv  string `json:"default_env"`
}

// ── Global config ─────────────────────────────────────────────────────────────

func globalConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, configDirName, globalFile), nil
}

func LoadGlobal() (*GlobalConfig, error) {
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
	data, err := json.MarshalIndent(cfg, "", "  ")
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
	return os.Remove(path)
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
