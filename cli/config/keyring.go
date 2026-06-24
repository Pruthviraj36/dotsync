//go:build !linux || (linux && cgo)

package config

// keyring.go — stores project passwords in the OS keychain.
//
// This file is built when CGO is available (macOS, Windows, Linux with CGO).
// On Linux without CGO (cross-compiled binaries), keyring_nocgo.go is used
// instead, which falls back to DOTSYNC_PASSWORD env var only.
//
// Storage backends:
//   macOS   → Keychain
//   Linux   → libsecret / GNOME Keyring (requires CGO + libsecret-1-dev)
//   Windows → Windows Credential Manager

import (
	"errors"
	"fmt"
	"os"

	"github.com/zalando/go-keyring"
)

const (
	keyringService       = "dotsync"
	keyringAccountPrefix = "project:"
)

var ErrNoPassword = errors.New(
	"no project password found\n" +
		"  On this machine for the first time? Run: dotsync init\n" +
		"  In CI/CD? Set the DOTSYNC_PASSWORD environment variable.",
)

func GetProjectPassword(projectSlug string) (string, error) {
	if p := os.Getenv("DOTSYNC_PASSWORD"); p != "" {
		return p, nil
	}

	account := keyringAccountPrefix + projectSlug
	password, err := keyring.Get(keyringService, account)
	if err == nil {
		return password, nil
	}

	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNoPassword
	}

	return "", fmt.Errorf(
		"could not access OS keychain (%w)\n"+
			"  On a headless Linux server? Set DOTSYNC_PASSWORD instead.", err)
}

func SetProjectPassword(projectSlug, password string) error {
	account := keyringAccountPrefix + projectSlug
	if err := keyring.Set(keyringService, account, password); err != nil {
		return fmt.Errorf(
			"could not save password to OS keychain (%w)\n"+
				"  On a headless Linux server? Use the DOTSYNC_PASSWORD env var instead.", err)
	}
	return nil
}

func DeleteProjectPassword(projectSlug string) error {
	account := keyringAccountPrefix + projectSlug
	err := keyring.Delete(keyringService, account)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete keychain entry: %w", err)
	}
	return nil
}
