package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "dotsync",
	Short: "🔐 Sync .env secrets securely across your team",
	Long: `DotSync — end-to-end encrypted secret sync for dev teams.

Secrets are encrypted on your machine before they ever reach the server.
The server only stores encrypted blobs — it never sees your raw values.

Get started:
  dotsync login       Authenticate with GitHub
  dotsync init        Link this folder to a project
  dotsync push        Upload your .env (encrypted)
  dotsync pull        Download latest .env
  dotsync run         Run a command with secrets injected (nothing hits disk)
  dotsync scan        Scan for secrets accidentally left in source files`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(
		versionCmd(),
		loginCmd(),
		logoutCmd(),
		initCmd(),
		pushCmd(),
		pullCmd(),
		runCmd(),
		rollbackCmd(),
		historyCmd(),
		diffCmd(),
		scanCmd(),
		envsCmd(),
		statusCmd(),
		auditCmd(),
		configCmd(),
		updateCmd(),
		teamCmd(),
	)
}
