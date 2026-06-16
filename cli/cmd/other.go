package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/Pruthviraj36/dotsync/cli/api"
	cliCrypto "github.com/Pruthviraj36/dotsync/cli/crypto"
	"github.com/Pruthviraj36/dotsync/cli/config"
)

func historyCmd() *cobra.Command {
	var envFlag string

	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show version history for this environment",
		Example: `  dotsync history
  dotsync history --env production`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireLogin()
			if err != nil {
				return err
			}

			projCfg, err := config.LoadProject()
			if err != nil {
				return err
			}

			env := envFlag
			if env == "" {
				env = projCfg.DefaultEnv
			}

			client := api.New(cfg)
			history, err := client.History(projCfg.ProjectSlug, env)
			if err != nil {
				return err
			}

			if len(history) == 0 {
				fmt.Println("No history yet. Run: dotsync push")
				return nil
			}

			fmt.Printf("\n📜 History for %s/%s\n", projCfg.ProjectSlug, env)
			fmt.Println(strings.Repeat("─", 50))

			for i, entry := range history {
				t, _ := time.Parse(time.RFC3339, entry.CreatedAt)
				age := formatAge(t)

				prefix := "  "
				if i == 0 {
					prefix = "→ " // current version
				}

				fmt.Printf("%sv%-3d  %-20s  by @%s\n",
					prefix, entry.Version, age, entry.PushedBy)
			}

			fmt.Println(strings.Repeat("─", 50))
			fmt.Printf("  %d version(s) shown\n\n", len(history))
			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	return cmd
}

func diffCmd() *cobra.Command {
	var envFlag string

	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Show what changed between your local .env and the remote version",
		Long: `Compares your local .env file with the latest remote version.
Only shows which keys changed — values are never displayed.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireLogin()
			if err != nil {
				return err
			}

			projCfg, err := config.LoadProject()
			if err != nil {
				return err
			}

			env := envFlag
			if env == "" {
				env = projCfg.DefaultEnv
			}

			// Read local
			localData, err := os.ReadFile(".env")
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Println("No local .env found — run: dotsync pull")
					return nil
				}
				return err
			}

			// Fetch remote
			client := api.New(cfg)
			remote, err := client.Pull(projCfg.ProjectSlug, env)
			if err != nil {
				return fmt.Errorf("fetch remote: %w", err)
			}

			remotePlain, err := cliCrypto.DecryptEnvFile(
				remote.EncryptedData, remote.Nonce,
				cfg.AccessToken, projCfg.ProjectSlug,
			)
			if err != nil {
				return fmt.Errorf("decrypt remote: %w", err)
			}

			localMap := cliCrypto.ParseEnvFile(string(localData))
			remoteMap := cliCrypto.ParseEnvFile(remotePlain)

			// local vs remote: what would change if you pushed?
			added, removed, changed := cliCrypto.DiffEnvFiles(remoteMap, localMap)

			fmt.Printf("\n🔍 Diff: local .env ↔ remote %s/%s (v%d)\n",
				projCfg.ProjectSlug, env, remote.Version)
			fmt.Println(strings.Repeat("─", 50))

			if len(added)+len(removed)+len(changed) == 0 {
				fmt.Println("  ✅ No differences — your .env is in sync.")
				fmt.Println()
				return nil
			}

			for _, k := range added {
				fmt.Printf("  + %-30s (new key, only in local)\n", k)
			}
			for _, k := range removed {
				fmt.Printf("  - %-30s (removed locally)\n", k)
			}
			for _, k := range changed {
				fmt.Printf("  ~ %-30s (value changed)\n", k)
			}

			fmt.Println(strings.Repeat("─", 50))
			fmt.Printf("  +%d added  -%d removed  ~%d changed\n\n",
				len(added), len(removed), len(changed))
			fmt.Println("  Run 'dotsync push' to upload your local changes.")
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment to compare against")
	return cmd
}

func envsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "envs",
		Short: "List environments for this project",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := requireLogin()
			if err != nil {
				return err
			}

			projCfg, err := config.LoadProject()
			if err != nil {
				return err
			}

			fmt.Printf("\n🌍 Environments for project '%s'\n\n", projCfg.ProjectSlug)
			envs := []struct{ name, desc string }{
				{"dev", "local development (default)"},
				{"staging", "pre-production testing"},
				{"production", "live environment (restricted)"},
			}

			for _, e := range envs {
				marker := "  "
				if e.name == projCfg.DefaultEnv {
					marker = "→ "
				}
				fmt.Printf("%s%-12s %s\n", marker, e.name, e.desc)
			}

			fmt.Println()
			fmt.Println("Usage:")
			fmt.Println("  dotsync push --env production")
			fmt.Println("  dotsync pull --env staging")
			fmt.Println()
			return nil
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current login and project status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _ := config.LoadGlobal()
			projCfg, projErr := config.LoadProject()

			fmt.Println()
			fmt.Println("📊 DotSync Status")
			fmt.Println(strings.Repeat("─", 40))

			if config.IsLoggedIn(cfg) {
				fmt.Printf("  User    : @%s ✅\n", cfg.Username)
				fmt.Printf("  Server  : %s\n", cfg.ServerURL)
			} else {
				fmt.Println("  User    : not logged in ❌")
				fmt.Println("  Run: dotsync login")
			}

			fmt.Println()

			if projErr == nil {
				fmt.Printf("  Project : %s ✅\n", projCfg.ProjectSlug)
				fmt.Printf("  Env     : %s (default)\n", projCfg.DefaultEnv)
			} else {
				fmt.Println("  Project : not linked ❌")
				fmt.Println("  Run: dotsync init")
			}

			fmt.Println(strings.Repeat("─", 40))
			fmt.Println()
			return nil
		},
	}
}

func formatAge(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("2006-01-02")
	}
}
