package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
	cliCrypto "github.com/Pruthviraj36/dotsync/cli/crypto"
	"github.com/spf13/cobra"
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

			fmt.Printf("\n"+bold("📜 History for %s/%s")+"\n", projCfg.ProjectSlug, env)
			fmt.Println(strings.Repeat("─", 50))

			for i, entry := range history {
				t, _ := time.Parse(time.RFC3339, entry.CreatedAt)
				age := formatAge(t)

				prefix := "  "
				if i == 0 {
					prefix = green("→ ") // current version
				}

				fmt.Printf("%s"+green("v%-3d")+"  "+dim("%-20s")+"  by "+cyan("@%s")+"\n",
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

			var diffPassword string
			if cfg.ProjectPasswords != nil {
				diffPassword = cfg.ProjectPasswords[projCfg.ProjectSlug]
			}
			if envPass := os.Getenv("DOTSYNC_PASSWORD"); envPass != "" {
				diffPassword = envPass
			}
			if diffPassword == "" {
				return fmt.Errorf("missing project password — run: dotsync init --rotate-password or set DOTSYNC_PASSWORD")
			}
			remotePlain, err := cliCrypto.DecryptEnvFile(
				remote.EncryptedData, remote.Nonce,
				diffPassword, projCfg.ProjectSlug,
			)
			if err != nil {
				return fmt.Errorf("decrypt remote: %w", err)
			}

			localMap := cliCrypto.ParseEnvFile(string(localData))
			remoteMap := cliCrypto.ParseEnvFile(remotePlain)

			// local vs remote: what would change if you pushed?
			added, removed, changed := cliCrypto.DiffEnvFiles(remoteMap, localMap)

			fmt.Printf("\n"+bold("🔍 Diff: local .env ↔ remote %s/%s")+" ("+green("v%d")+")\n",
				projCfg.ProjectSlug, env, remote.Version)
			fmt.Println(strings.Repeat("─", 50))

			if len(added)+len(removed)+len(changed) == 0 {
				fmt.Println("  "+green("✅ No differences — your .env is in sync."))
				fmt.Println()
				return nil
			}

			for _, k := range added {
				fmt.Printf("  "+green("+ %-30s")+" (new key, only in local)\n", k)
			}
			for _, k := range removed {
				fmt.Printf("  "+red("- %-30s")+" (removed locally)\n", k)
			}
			for _, k := range changed {
				fmt.Printf("  "+yellow("~ %-30s")+" (value changed)\n", k)
			}

			fmt.Println(strings.Repeat("─", 50))
			fmt.Printf("  "+green("+%d added")+"  "+red("-%d removed")+"  "+yellow("~%d changed")+"\n\n",
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
			cfg, err := requireLogin()
			if err != nil {
				return err
			}

			projCfg, err := config.LoadProject()
			if err != nil {
				return err
			}

			client := api.New(cfg)
			envs, err := client.ListEnvironments(projCfg.ProjectSlug)
			if err != nil {
				// fallback to known defaults if server unreachable
				envs = []string{"dev", "staging", "production"}
			}

			fmt.Printf("\n"+bold("🌍 Environments for '%s'")+"\n\n", projCfg.ProjectSlug)
			for _, e := range envs {
				marker := "  "
				if e == projCfg.DefaultEnv {
					marker = green("→ ")
				}
				fmt.Printf("%s%s\n", marker, cyan(e))
			}

			fmt.Println()
			fmt.Printf("  %s\n", dim("dotsync push --env production"))
			fmt.Printf("  %s\n", dim("dotsync pull --env staging"))
			fmt.Println()
			return nil
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current login, project, and sync state",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _ := config.LoadGlobal()
			projCfg, projErr := config.LoadProject()

			fmt.Println()
			fmt.Println(bold("📊 DotSync Status"))
			fmt.Println(strings.Repeat("─", 44))

			// Auth state
			if config.IsLoggedIn(cfg) {
				fmt.Printf("  "+bold("User")+"    : "+cyan("@%s")+" "+green("✅")+"\n", cfg.Username)
				fmt.Printf("  Server  : %s\n", cfg.ServerURL)
			} else {
				fmt.Println("  "+bold("User")+"    : "+red("not logged in ❌"))
				fmt.Println("  Run: dotsync login")
				fmt.Println(strings.Repeat("─", 44))
				fmt.Println()
				return nil
			}

			fmt.Println()

			if projErr != nil {
				fmt.Println("  "+bold("Project")+" : "+red("not linked ❌"))
				fmt.Println("  Run: dotsync init")
				fmt.Println(strings.Repeat("─", 44))
				fmt.Println()
				return nil
			}

			fmt.Printf("  "+bold("Project")+" : "+cyan("%s")+" "+green("✅")+"\n", projCfg.ProjectSlug)
			fmt.Printf("  Env     : %s (default)\n", projCfg.DefaultEnv)

			// Keychain password state
			_, pwErr := config.GetProjectPassword(projCfg.ProjectSlug)
			if pwErr != nil {
				fmt.Println("  "+bold("Password")+": "+red("❌ not in keychain")+" — run: "+cyan("dotsync init --rotate-password"))
			} else {
				fmt.Println("  "+bold("Password")+": "+green("🔑 in OS keychain ✅"))
			}

			fmt.Println()

			// Sync state — compare remote version with local .env existence
			client := api.New(cfg)
			remoteVer, pushedBy, err := client.GetLatestVersion(
				projCfg.ProjectSlug, projCfg.DefaultEnv)
			if err != nil {
				fmt.Println("  "+bold("Sync")+"    : "+yellow("⚠️  could not reach server"))
			} else if remoteVer == 0 {
				fmt.Println("  Sync    : no secrets pushed yet")
				fmt.Println("  Run: dotsync push")
			} else {
				fmt.Printf("  "+bold("Remote")+"  : "+green("v%d")+" (by "+cyan("@%s")+")\n", remoteVer, pushedBy)
				if _, err := os.Stat(".env"); err == nil {
					fmt.Println("  Local   : .env exists — run 'dotsync diff' to compare")
				} else {
					fmt.Println("  Local   : no .env ⚠️  — run: dotsync pull")
				}
			}

			// Team size
			members, err := client.ListTeamMembers(projCfg.ProjectSlug)
			if err == nil {
				fmt.Printf("  Team    : %d member(s) — 'dotsync team list' for details\n",
					len(members))
			}

			fmt.Println(strings.Repeat("─", 44))
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
