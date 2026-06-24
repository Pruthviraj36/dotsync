package config

// keyring.go — password helpers used by push, pull, run, rollback, and status.
//
// Passwords are stored in ~/.dotsync/config.json under project_passwords map.
// The file is created with 0600 permissions (owner read/write only).
//
// CI/CD: set DOTSYNC_PASSWORD env var to bypass the stored map entirely.

import (
	"errors"
	"os"
)

// ErrNoPassword is returned when no password is found for the project.
var ErrNoPassword = errors.New(
	"no project password found\n" +
		"  On this machine for the first time? Run: dotsync init --rotate-password\n" +
		"  In CI/CD? Set the DOTSYNC_PASSWORD environment variable.",
)

// GetProjectPassword returns the password for the given project slug.
// Resolution order:
//  1. DOTSYNC_PASSWORD env var (CI/CD)
//  2. ~/.dotsync/config.json project_passwords map
func GetProjectPassword(projectSlug string) (string, error) {
	if p := os.Getenv("DOTSYNC_PASSWORD"); p != "" {
		return p, nil
	}

	cfg, err := LoadGlobal()
	if err != nil {
		return "", err
	}

	if cfg.ProjectPasswords != nil {
		if p, ok := cfg.ProjectPasswords[projectSlug]; ok && p != "" {
			return p, nil
		}
	}

	return "", ErrNoPassword
}

// SetProjectPassword stores the password for a project in the global config.
func SetProjectPassword(projectSlug, password string) error {
	cfg, err := LoadGlobal()
	if err != nil {
		return err
	}

	if cfg.ProjectPasswords == nil {
		cfg.ProjectPasswords = make(map[string]string)
	}
	cfg.ProjectPasswords[projectSlug] = password

	return SaveGlobal(cfg)
}
