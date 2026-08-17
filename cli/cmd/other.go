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

			// Pre-compute display values and column widths
			type row struct{ ver, age, who string }
			rows := make([]row, len(history))
			verW, ageW, whoW := 7, 4, 3 // min widths
			for i, e := range history {
				t, _ := time.Parse(time.RFC3339, e.CreatedAt)
				r := row{
					ver: fmt.Sprintf("v%d", e.Version),
					age: formatAge(t),
					who: "@" + e.PushedBy,
				}
				rows[i] = r
				if w := len(r.ver); w > verW { verW = w }
				if w := len(r.age); w > ageW { ageW = w }
				if w := len(r.who); w > whoW { whoW = w }
			}

			rw := verW + ageW + whoW + 10
			rl := ruleN(rw)

			blank()
			fmt.Printf("  %s  %s/%s\n", bold("Version History"), boldCyan(projCfg.ProjectSlug), cyan(env))
			blank()
			tableHeader(rl,
				[]string{"VERSION", "WHEN", "BY"},
				[]int{verW, ageW, whoW},
			)

			for i, r := range rows {
				marker := "   "
				verColored := colDim(r.ver, verW)
				if i == 0 {
					marker = green(" ▶ ")
					verColored = padRight(boldGreen(r.ver), verW)
				}
				tableRow(marker,
					verColored,
					colDim(r.age, ageW),
					cyan(r.who),
				)
			}

			fmt.Printf("%s%s\n", strings.Repeat(" ", labelW+2), rl)
			fmt.Printf("  %s version(s)  ·  %s to restore: %s\n",
				dim(fmt.Sprintf("%d", len(history))),
				dim("rollback"),
				cyan(fmt.Sprintf("dotsync rollback %d", history[len(history)-1].Version)),
			)
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

			rw := 56
			rl := ruleN(rw)

			blank()
			fmt.Printf("  %s  local .env vs %s/%s (%s)\n",
				bold("Diff"), boldCyan(projCfg.ProjectSlug), cyan(env), green(fmt.Sprintf("v%d", remote.Version)))
			blank()
			fmt.Printf("%s%s\n", strings.Repeat(" ", labelW+2), rl)

			if len(added)+len(removed)+len(changed) == 0 {
				blank()
				fmt.Println("  " + ok("No differences — local is in sync with remote."))
				blank()
				return nil
			}

			for _, k := range added {
				fmt.Printf("  %s  %s\n", green("+"), boldGreen(k))
			}
			for _, k := range removed {
				fmt.Printf("  %s  %s\n", red("−"), red(k))
			}
			for _, k := range changed {
				fmt.Printf("  %s  %s\n", yellow("~"), yellow(k))
			}

			fmt.Printf("%s%s\n", strings.Repeat(" ", labelW+2), rl)
			parts := []string{}
			if len(added) > 0   { parts = append(parts, green(fmt.Sprintf("+%d added", len(added)))) }
			if len(removed) > 0 { parts = append(parts, red(fmt.Sprintf("−%d removed", len(removed)))) }
			if len(changed) > 0 { parts = append(parts, yellow(fmt.Sprintf("~%d changed", len(changed)))) }
			fmt.Printf("  %s\n", strings.Join(parts, "  "))
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

			blank()
			fmt.Printf("  %s  %s\n", bold("Environments"), boldCyan(projCfg.ProjectSlug))
			blank()

			for _, e := range envs {
				if e == projCfg.DefaultEnv {
					fmt.Printf("  %s %s  %s\n", green("▶"), boldGreen(e), dim("(default)"))
				} else {
					fmt.Printf("    %s\n", cyan(e))
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

			blank()
			fmt.Printf("  %s\n", bold("DotSync Status"))
			blank()

			// ── Auth ─────────────────────────────────────────────────────────
			if config.IsLoggedIn(cfg) {
				fmt.Printf("  %s  %s  %s\n",
					bold("Account"), cyan("@"+cfg.Username), dim("connected"))
				fmt.Printf("  %s  %s\n",
					bold("Server "), dim(cfg.ServerURL))
			} else {
				fmt.Printf("  %s  %s\n", bold("Account"), red("not logged in"))
				blank()
				hint("dotsync login")
				blank()
				return nil
			}

			blank()

			// ── Project ───────────────────────────────────────────────────────
			if projErr != nil {
				fmt.Printf("  %s  %s\n", bold("Project"), red("not linked"))
				blank()
				hint("dotsync init")
				blank()
				return nil
			}

			fmt.Printf("  %s  %s\n", bold("Project"), boldCyan(projCfg.ProjectSlug))
			fmt.Printf("  %s  %s\n", bold("Env    "), cyan(projCfg.DefaultEnv))

			client := api.New(cfg)

			// ── Password ──────────────────────────────────────────────────────
			_, pwErr := resolvePassword(client, projCfg.ProjectSlug)
			if pwErr != nil {
				fmt.Printf("  %s  %s\n", bold("Password"), red("not set  ")+dim("→ dotsync init --rotate-password"))
			} else {
				fmt.Printf("  %s  %s\n", bold("Password"), green("available"))
			}

			blank()

			// ── Remote state ──────────────────────────────────────────────────
			remoteVer, pushedBy, err := client.GetLatestVersion(
				projCfg.ProjectSlug, projCfg.DefaultEnv)
			if err != nil {
				fmt.Printf("  %s  %s\n", bold("Remote  "), yellow("could not reach server"))
			} else if remoteVer == 0 {
				fmt.Printf("  %s  %s\n", bold("Remote  "), dim("no secrets pushed yet"))
				blank()
				hint("dotsync push")
			} else {
				fmt.Printf("  %s  %s  %s\n",
					bold("Remote  "),
					green(fmt.Sprintf("v%d", remoteVer)),
					dim("pushed by @"+pushedBy),
				)
				if _, err := os.Stat(".env"); err == nil {
					fmt.Printf("  %s  %s\n", bold("Local   "), green(".env present  ")+dim("→ dotsync diff"))
				} else {
					fmt.Printf("  %s  %s\n", bold("Local   "), yellow("no .env  ")+dim("→ dotsync pull"))
				}
			}

			// ── Team ─────────────────────────────────────────────────────────
			members, err := client.ListTeamMembers(projCfg.ProjectSlug)
			if err == nil {
				blank()
				fmt.Printf("  %s  %s\n",
					bold("Team    "),
					dim(fmt.Sprintf("%d member(s)  → dotsync team list", len(members))),
				)
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
