package cmd

import (
	"fmt"
	"os"

	"github.com/Pruthviraj36/dotsync/cli/api"
)

// resolvePassword returns the E2EE password for a project. Resolution order:
//  1. DOTSYNC_PASSWORD env var — always wins, for CI/CD.
//  2. The server, via the authenticated API client — the password is stored
//     encrypted there (see internal/service.PasswordService) rather than in
//     a local file, so this works on any machine without re-typing it.
func resolvePassword(client *api.Client, projectSlug string) (string, error) {
	if p := os.Getenv("DOTSYNC_PASSWORD"); p != "" {
		return p, nil
	}

	password, err := client.GetProjectPassword(projectSlug)
	if err != nil {
		return "", fmt.Errorf(
			"could not get project password: %w\n"+
				"  First time on this project? Run: dotsync init --rotate-password\n"+
				"  In CI/CD? Set the DOTSYNC_PASSWORD environment variable.",
			err,
		)
	}
	return password, nil
}

// setPassword pushes a new/rotated password to the server for the project.
func setPassword(client *api.Client, projectSlug, password string) error {
	return client.SetProjectPassword(projectSlug, password)
}
