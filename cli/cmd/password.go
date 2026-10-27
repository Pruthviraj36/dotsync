package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
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

// passwordCmd groups password lifecycle commands.
func passwordCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "password",
		Short: "Manage the project encryption password",
	}
	cmd.AddCommand(passwordRotateCmd())
	return cmd
}

func passwordRotateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate",
		Short: "Change the project encryption password",
		Long: `Sets a new encryption password for the linked project.

By default the password is stored server-side encrypted with the server's
master key, so teammates can fetch it automatically. Existing secret
versions stay decryptable with DOTSYNC_PASSWORD or the old password, so
re-push after rotating if you need every environment re-encrypted.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireLogin()
			if err != nil {
				return err
			}
			projCfg, err := config.LoadProject()
			if err != nil {
				return err
			}

			password, err := readPassword(" New encryption password: ")
			if err != nil {
				return err
			}
			confirm, err := readPassword(" Confirm password: ")
			if err != nil {
				return err
			}
			if confirm != password {
				return fmt.Errorf("passwords do not match")
			}
			if len(password) < minProjectPasswordLen {
				return fmt.Errorf("password must be at least %d characters", minProjectPasswordLen)
			}

			client := api.New(cfg)
			if err := setPassword(client, projCfg.ProjectSlug, password); err != nil {
				return fmt.Errorf("rotate password: %w", err)
			}
			complete("Project password rotated — re-push environments you want re-encrypted")
			return nil
		},
	}
}

// setPassword pushes a new or rotated password to the server.
// Only called by the owner at project creation time (dotsync init).
func setPassword(client *api.Client, projectSlug, password string) error {
	return client.SetProjectPassword(projectSlug, password)
}
