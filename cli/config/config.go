package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	ProjectSlug     string `json:"project_slug"`
	DefaultEnv      string `json:"default_env"`
	ProjectPassword string `json:"project_password,omitempty"`
}

// ── Global config ────────────────────────────────────────────────────────────

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
		return &GlobalConfig{ServerURL: DefaultServerURL()}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg GlobalConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.ServerURL == "" {
		cfg.ServerURL = DefaultServerURL()
	}

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

	// 0600 — only owner can read/write credentials file
	return os.WriteFile(path, data, 0600)
}

func ClearGlobal() error {
	path, err := globalConfigPath()
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// ── Project config ───────────────────────────────────────────────────────────

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

// DefaultServerURL returns the server URL, overridable via DOTSYNC_SERVER env var.
func DefaultServerURL() string {
	if url := os.Getenv("DOTSYNC_SERVER"); url != "" {
		return url
	}
	return "https://dotsync.onrender.com"
}

// IsLoggedIn checks if there's a valid stored token.
func IsLoggedIn(cfg *GlobalConfig) bool {
	return cfg != nil && cfg.AccessToken != ""
}
