package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "dotsync",
	Short: "Sync .env secrets securely across your team",
	Long: `DotSync — client-encrypted secret sync for dev teams.

Secrets are encrypted on your machine before they reach the server.
Project keys are wrapped for individual member devices; the server stores
ciphertext, public keys, and opaque key envelopes.

Get started:
  dotsync login       Authenticate with GitHub
  dotsync account-password set
  dotsync init        Link this folder to a project
  dotsync push        Upload your .env (encrypted)
  dotsync pull        Download latest .env
  dotsync run         Run a command with secrets injected (nothing hits disk)
  dotsync ui          Open the web dashboard (localhost:4040)

CI/CD integrations:
  dotsync integrate github-actions   GitHub Actions workflow snippet
  dotsync integrate vercel           Vercel environment sync
  dotsync integrate railway          Railway deployment snippet
  dotsync integrate netlify          Netlify build environment
  dotsync integrate docker           Docker / Compose snippet
  dotsync integrate shell            Bash/Zsh/Fish export snippet

Service tokens:
  dotsync tokens create   Create a CI/CD service token
  dotsync tokens list     List service tokens for this project
  dotsync tokens revoke   Revoke a service token`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() {
	// Hide the auto-generated completion command — it clutters the help output.
	// Power users can still run `dotsync completion bash` etc directly.
	if c, _, err := rootCmd.Find([]string{"completion"}); err == nil && c != nil {
		c.Hidden = true
	}
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(
		versionCmd(),
		loginCmd(),
		accountPasswordCmd(),
		logoutCmd(),
		initCmd(),
		pushCmd(),
		pullCmd(),
		runCmd(),
		rollbackCmd(),
		historyCmd(),
		diffCmd(),
		envsCmd(),
		statusCmd(),
		auditCmd(),
		configCmd(),
		updateCmd(),
		teamCmd(),
		integrateCmd(),
		tokensCmd(),
		passwordCmd(),
		uiCmd(),
	)
}
