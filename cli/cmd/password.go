package cmd

import (
	"fmt"
	"os"

	"github.com/Pruthviraj36/dotsync/cli/api"
)

// resolvePassword returns the E2EE encryption password for a project.
//
// Resolution order:
//  1. DOTSYNC_PASSWORD env var — always wins, designed for CI/CD pipelines
//     where interactive prompts are not possible.
//  2. Server fetch — the password is stored encrypted on the server
//     (AES-256-GCM with SERVER_MASTER_KEY). Any authenticated team member
//     can fetch it. No one needs to re-type it on a new machine.
//
// The password is NEVER stored locally. It is fetched on every push/pull.
// Owners set it once at project creation via dotsync init.
// Invited members get it automatically — no manual step required.
func resolvePassword(client *api.Client, projectSlug string) (string, error) {
	if p := os.Getenv("DOTSYNC_PASSWORD"); p != "" {
		return p, nil
	}
	password, err := client.GetProjectPassword(projectSlug)
	if err != nil {
		return "", fmt.Errorf(
			"could not fetch project password: %w\n\n"+
				"  If you are the project owner and this is a new project:\n"+
				"    dotsync init  (creates the project and sets the password)\n\n"+
				"  If you were invited to an existing project:\n"+
				"    Ask the owner to push at least once first.\n\n"+
				"  For CI/CD pipelines, set the DOTSYNC_PASSWORD env var.",
			err,
		)
	}
	return password, nil
}

// setPassword pushes a new or rotated password to the server.
// Only called by the owner at project creation time (dotsync init).
func setPassword(client *api.Client, projectSlug, password string) error {
	return client.SetProjectPassword(projectSlug, password)
}
