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

// ── history ───────────────────────────────────────────────────────────────────

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
				blank()
				fmt.Println(info("No history yet."))
				cmdHint("dotsync push")
				blank()
				return nil
			}

			sectionTitle("History · " + projCfg.ProjectSlug + "/" + env)
			for i, entry := range history {
				when, _ := time.Parse(time.RFC3339, entry.CreatedAt)
				version := fmt.Sprintf("v%d", entry.Version)
				if i == 0 {
					item(boldGreen("● "+version+"  current"), "pushed by @"+entry.PushedBy+" · "+formatAge(when))
				} else {
					item(cyan(version), "pushed by @"+entry.PushedBy+" · "+formatAge(when)+" · restore: dotsync rollback "+fmt.Sprint(entry.Version))
				}
			}
			blank()
			hint(fmt.Sprintf("%d versions · restore any earlier version with: dotsync rollback <version>", len(history)))
			blank()
			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	return cmd
}

// ── diff ─────────────────────────────────────────────────────────────────────

func diffCmd() *cobra.Command {
	var envFlag string

	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Show what changed between your local .env and remote",
		Long: `Compares your local .env with the latest remote version.
Key names are shown; values are never displayed.`,
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

			localData, err := os.ReadFile(".env")
			if err != nil {
				if os.IsNotExist(err) {
					blank()
					fmt.Println(warn("No local .env found."))
					cmdHint("dotsync pull")
					blank()
					return nil
				}
				return err
			}

			fmt.Println(prog("Fetching", boldCyan(projCfg.ProjectSlug+"/"+env)))

			client := api.New(cfg)
			remote, err := client.Pull(projCfg.ProjectSlug, env)
			if err != nil {
				return fmt.Errorf("fetch remote: %w", err)
			}

			password, err := resolvePassword(client, projCfg.ProjectSlug)
			if err != nil {
				return err
			}
			remotePlain, err := cliCrypto.DecryptEnvFile(
				remote.EncryptedData, remote.Nonce, password, projCfg.ProjectSlug,
			)
			if err != nil {
				return fmt.Errorf("decrypt remote: %w", err)
			}

			localMap := cliCrypto.ParseEnvFile(string(localData))
			remoteMap := cliCrypto.ParseEnvFile(remotePlain)
			added, removed, changed := cliCrypto.DiffEnvFiles(remoteMap, localMap)

			sectionTitle(fmt.Sprintf("Diff · %s/%s", projCfg.ProjectSlug, env))
			fmt.Printf("  %s\n", dim("local .env vs remote "+fmt.Sprintf("v%d", remote.Version)))

			if len(added)+len(removed)+len(changed) == 0 {
				blank()
				fmt.Println("  " + ok("No differences — local is in sync with remote."))
				blank()
				return nil
			}

			for _, k := range added {
				item(green("added · ")+boldGreen(k), "present remotely but missing from local .env")
			}
			for _, k := range removed {
				item(red("removed · ")+red(k), "present locally but missing from remote")
			}
			for _, k := range changed {
				item(yellow("changed · ")+yellow(k), "key exists in both places with different values")
			}

			parts := []string{}
			if len(added) > 0 {
				parts = append(parts, green(fmt.Sprintf("+%d added", len(added))))
			}
			if len(removed) > 0 {
				parts = append(parts, red(fmt.Sprintf("−%d removed", len(removed))))
			}
			if len(changed) > 0 {
				parts = append(parts, yellow(fmt.Sprintf("~%d changed", len(changed))))
			}
			item(strings.Join(parts, " · "), "summary")
			blank()
			hint("Push local changes: dotsync push")
			blank()
			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment to compare against")
	return cmd
}

// ── envs ─────────────────────────────────────────────────────────────────────

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
				// fallback — server may not have envs yet
				envs = []string{"dev", "staging", "production"}
			}

			sectionTitle("Environments · " + projCfg.ProjectSlug)

			for _, e := range envs {
				if e == projCfg.DefaultEnv {
					item(boldGreen(e)+"  (default)", "active project environment")
				} else {
					item(cyan(e), "available environment")
				}
			}

			blank()
			hint("dotsync push --env production")
			hint("dotsync pull --env staging")
			blank()
			return nil
		},
	}
}

// ── status ───────────────────────────────────────────────────────────────────

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current login, project, and sync state",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _ := config.LoadGlobal()
			projCfg, projErr := config.LoadProject()

			sectionTitle("DotSync status")

			// ── Auth ─────────────────────────────────────────────────────────
			if config.IsLoggedIn(cfg) {
				kvCyan("account", "@"+cfg.Username+" · connected")
				kvDim("server", cfg.ServerURL)
			} else {
				kvRed("account", "not logged in")
				blank()
				hint("dotsync login")
				blank()
				return nil
			}

			blank()

			// ── Project ───────────────────────────────────────────────────────
			if projErr != nil {
				kvRed("project", "not linked")
				blank()
				hint("dotsync init")
				blank()
				return nil
			}

			kvCyan("project", projCfg.ProjectSlug)
			kvCyan("environment", projCfg.DefaultEnv)

			client := api.New(cfg)

			// ── Password ──────────────────────────────────────────────────────
			_, pwErr := resolvePassword(client, projCfg.ProjectSlug)
			if pwErr != nil {
				kv("password", red("not set")+dim(" → dotsync init --rotate-password"))
			} else {
				kvGreen("password", "available")
			}

			blank()

			// ── Remote state ──────────────────────────────────────────────────
			remoteVer, pushedBy, err := client.GetLatestVersion(
				projCfg.ProjectSlug, projCfg.DefaultEnv)
			if err != nil {
				kv("remote", yellow("could not reach server"))
			} else if remoteVer == 0 {
				kvDim("remote", "no secrets pushed yet")
				blank()
				hint("dotsync push")
			} else {
				kv("remote", green(fmt.Sprintf("v%d", remoteVer))+dim(" · pushed by @"+pushedBy))
				if _, err := os.Stat(".env"); err == nil {
					kv("local", green(".env present")+dim(" → dotsync diff"))
				} else {
					kv("local", yellow("no .env")+dim(" → dotsync pull"))
				}
			}

			// ── Team ─────────────────────────────────────────────────────────
			members, err := client.ListTeamMembers(projCfg.ProjectSlug)
			if err == nil {
				blank()
				kvDim("team", fmt.Sprintf("%d member(s) → dotsync team list", len(members)))
			}

			blank()
			return nil
		},
	}
}

// ── formatAge ────────────────────────────────────────────────────────────────

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
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}
