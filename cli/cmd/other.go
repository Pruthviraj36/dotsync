package cmd

import (
	"fmt"
	"os"
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
				banner(fmt.Sprintf("Version History — %s/%s", projCfg.ProjectSlug, env))
				fmt.Println(" No history yet.")
				fmt.Println(" Push initial secrets with: dotsync push")
				return nil
			}

			banner(fmt.Sprintf("Version History — %s/%s", projCfg.ProjectSlug, env))
			rows := make([][]string, len(history))
			for i, entry := range history {
				when, _ := time.Parse(time.RFC3339, entry.CreatedAt)
				version := fmt.Sprintf("v%d", entry.Version)
				status := ""
				if i == 0 {
					status = "Current"
				}
				rows[i] = []string{version, "@" + entry.PushedBy, formatAge(when), status}
			}
			table([]string{"Version", "Author", "Age", "Status"}, rows)
			fmt.Printf(" %d version(s) found. Restore any version with: dotsync rollback <version>\n", len(history))
			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	return cmd
}

// ── diff ─────────────────────────────────────────────────────────────────────

func diffCmd() *cobra.Command {
	var envFlag string
	var fileFlag string

	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Show what changed between your local .env and remote",
		Long: `Compares your local .env with the latest remote version.
Key names are shown; values are never displayed.`,
		Example: `  dotsync diff
  dotsync diff --env production
  dotsync diff --file .env.local`,
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
			localFile := fileFlag
			if localFile == "" {
				localFile = ".env"
			}

			localData, err := os.ReadFile(localFile)
			if err != nil {
				if os.IsNotExist(err) {
					banner(fmt.Sprintf("Differences — %s/%s", projCfg.ProjectSlug, env))
					fmt.Printf(" Local file %s does not exist.\n", localFile)
					fmt.Println(" Download latest secrets with: dotsync pull")
					return nil
				}
				return err
			}

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

			banner(fmt.Sprintf("Differences — %s/%s (local %s vs remote v%d)", projCfg.ProjectSlug, env, localFile, remote.Version))

			if len(added)+len(removed)+len(changed) == 0 {
				fmt.Println(" No differences found — local file is in sync with remote.")
				complete("")
				return nil
			}

			var rows [][]string
			for _, k := range added {
				rows = append(rows, []string{"+ Added", k, "Present remotely, missing from local file"})
			}
			for _, k := range removed {
				rows = append(rows, []string{"- Removed", k, "Present locally, missing from remote"})
			}
			for _, k := range changed {
				rows = append(rows, []string{"~ Changed", k, "Key exists in both with different values"})
			}

			table([]string{"State", "Variable", "Details"}, rows)
			fmt.Printf(" Summary: %d added, %d removed, %d changed.\n", len(added), len(removed), len(changed))
			fmt.Println(" Push local changes with: dotsync push")
			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment to compare against")
	cmd.Flags().StringVarP(&fileFlag, "file", "f", "", "local file to compare (default: .env)")
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
				envs = []string{"dev", "staging", "production"}
			}

			banner(fmt.Sprintf("Environments — %s", projCfg.ProjectSlug))
			rows := make([][]string, len(envs))
			for i, e := range envs {
				status := "Available"
				if e == projCfg.DefaultEnv {
					status = "Default"
				}
				rows[i] = []string{e, status}
			}
			table([]string{"Environment", "Status"}, rows)
			fmt.Println(" Target an environment with: dotsync pull --env <name>")
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

			banner("DotSync Status")

			var rows [][]string

			// ── Auth ─────────────────────────────────────────────────────────
			if config.IsLoggedIn(cfg) {
				rows = append(rows, []string{"Account", "@" + cfg.Username + " (connected)"})
				rows = append(rows, []string{"Server", cfg.ServerURL})
			} else {
				rows = append(rows, []string{"Account", "Not logged in (run 'dotsync login')"})
				table([]string{"Property", "Value"}, rows)
				return nil
			}

			// ── Project ───────────────────────────────────────────────────────
			if projErr != nil {
				rows = append(rows, []string{"Project", "Not linked (run 'dotsync init')"})
				table([]string{"Property", "Value"}, rows)
				return nil
			}

			rows = append(rows, []string{"Project", projCfg.ProjectSlug})
			rows = append(rows, []string{"Default Env", projCfg.DefaultEnv})

			client := api.New(cfg)

			// ── Password ──────────────────────────────────────────────────────
			_, pwErr := resolvePassword(client, projCfg.ProjectSlug)
			if pwErr != nil {
				rows = append(rows, []string{"Password", "Not set (run 'dotsync init')"})
			} else {
				rows = append(rows, []string{"Password", "Available (server-stored key)"})
			}

			// ── Remote state ──────────────────────────────────────────────────
			remoteVer, pushedBy, err := client.GetLatestVersion(
				projCfg.ProjectSlug, projCfg.DefaultEnv)
			if err != nil {
				rows = append(rows, []string{"Remote State", "Could not reach server"})
			} else if remoteVer == 0 {
				rows = append(rows, []string{"Remote State", "No secrets pushed yet (run 'dotsync push')"})
			} else {
				rows = append(rows, []string{"Remote State", fmt.Sprintf("v%d (pushed by @%s)", remoteVer, pushedBy)})
			}

			// ── Local state ───────────────────────────────────────────────────
			if _, err := os.Stat(".env"); err == nil {
				rows = append(rows, []string{"Local State", ".env present (compare with 'dotsync diff')"})
			} else {
				rows = append(rows, []string{"Local State", "No local .env found (download with 'dotsync pull')"})
			}

			// ── Team ─────────────────────────────────────────────────────────
			members, err := client.ListTeamMembers(projCfg.ProjectSlug)
			if err == nil {
				rows = append(rows, []string{"Team Members", fmt.Sprintf("%d member(s)", len(members))})
			}

			table([]string{"Property", "Value"}, rows)
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
