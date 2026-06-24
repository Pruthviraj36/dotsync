//go:build linux && !cgo

package config

// keyring_nocgo.go — pure-Go fallback for cross-compiled Linux binaries.
//
// CGO is not available when cross-compiling (e.g. GoReleaser building
// linux/amd64 and linux/arm64 on a Windows or macOS host). The real
// keyring backend requires libsecret which is CGO-only.
//
// On a real Linux machine with CGO enabled, keyring.go is used instead
// and provides full OS keychain integration.
//
// For users running the cross-compiled binary on Linux:
//   - Set DOTSYNC_PASSWORD in their shell profile or CI environment.
//   - Or build from source with CGO enabled to get keychain support.

import (
	"errors"
	"fmt"
	"os"
)

const (
	keyringService       = "dotsync"
	keyringAccountPrefix = "project:"
)

var ErrNoPassword = errors.New(
	"no project password found\n" +
		"  Set DOTSYNC_PASSWORD in your environment, or build from source\n" +
		"  with CGO enabled for OS keychain support:\n" +
		"    CGO_ENABLED=1 go install github.com/Pruthviraj36/dotsync@latest",
)

// GetProjectPassword on this build only checks DOTSYNC_PASSWORD.
// Full keychain support requires a CGO-enabled build (go install from source).
func GetProjectPassword(projectSlug string) (string, error) {
	if p := os.Getenv("DOTSYNC_PASSWORD"); p != "" {
		return p, nil
	}
	return "", ErrNoPassword
}

func SetProjectPassword(projectSlug, password string) error {
	fmt.Println("⚠️  This binary was cross-compiled without CGO — cannot save to OS keychain.")
	fmt.Println("   Set DOTSYNC_PASSWORD in your shell profile instead:")
	fmt.Printf("   export DOTSYNC_PASSWORD='your-project-password'\n")
	fmt.Println()
	fmt.Println("   Or build from source with CGO for full keychain support:")
	fmt.Println("   CGO_ENABLED=1 go install github.com/Pruthviraj36/dotsync@latest")
	return fmt.Errorf("keychain unavailable in cross-compiled binary")
}

func DeleteProjectPassword(projectSlug string) error {
	// Nothing stored, nothing to delete
	_ = os.Getenv("DOTSYNC_PASSWORD") // suppress unused warning
	return nil
}
