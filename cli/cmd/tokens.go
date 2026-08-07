package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
)

func tokensCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tokens",
		Short: "Manage service tokens for CI/CD integrations",
		Long: `Service tokens give CI/CD pipelines read-only access to your secrets
without exposing your personal GitHub credentials.

Each token is scoped to a specific environment (or all envs) and is
shown only once at creation time. Store it immediately as a secret in
your CI/CD platform (GitHub Actions Secrets, Railway, Vercel, etc.).

The server stores only a SHA-256 hash of each token — the raw value
is never persisted and cannot be recovered after creation.`,
	}
	cmd.AddCommand(
		tokensCreateCmd(),
		tokensListCmd(),
		tokensRevokeCmd(),
	)
	return cmd
}

// ── dotsync tokens create ────────────────────────────────────────────────────

func tokensCreateCmd() *cobra.Command {
	var envFlag string
	var nameFlag string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new service token",
		Long: `Creates a scoped, read-only service token for CI/CD pipelines.

The token is shown exactly once — copy it immediately to your CI/CD
platform's secret store. It cannot be retrieved again.

Token format: dst_<64 random hex chars>
The "dst_" prefix lets the server fast-path token validation without
attempting JWT parsing, and makes service tokens visually distinct from
user tokens in logs and dashboards.`,
		Example: `  dotsync tokens create
  dotsync tokens create --env production
  dotsync tokens create --env production --name github-actions-prod`,
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

			name := nameFlag
			if name == "" {
				name = "ci-" + env + "-" + time.Now().Format("20060102")
			}

			client := api.New(cfg)
			token, err := client.CreateServiceToken(projCfg.ProjectSlug, env, name)
			if err != nil {
				return fmt.Errorf("create token: %w", err)
			}

			rule := strings.Repeat("─", 64)

			fmt.Println()
			fmt.Println(ok("Service token created."))
			fmt.Println()
			fmt.Println(rule)
			fmt.Println()
			fmt.Printf("  %s\n", bold("Token"))
			fmt.Printf("  %s\n\n", cyan(token))
			fmt.Printf("  %s  %s\n", bold("Project:"), projCfg.ProjectSlug)
			fmt.Printf("  %s  %s\n", bold("Env:    "), env)
			fmt.Printf("  %s  %s\n", bold("Name:   "), name)
			fmt.Println()
			fmt.Println(rule)
			fmt.Println()
			fmt.Println(warn("This token is shown ONCE. Copy it now — it cannot be retrieved again."))
			fmt.Println()
			fmt.Println("  Add it to your CI/CD platform as a secret named DOTSYNC_TOKEN.")
			fmt.Println()
			fmt.Println("  Then in CI, pull secrets with:")
			fmt.Println()
			fmt.Printf("    %s\n", cyan("dotsync pull --env "+env+" --force"))
			fmt.Println()
			fmt.Println("  Or generate a full CI snippet:")
			fmt.Println()
			fmt.Printf("    %s\n", cyan("dotsync integrate github-actions --env "+env))
			fmt.Printf("    %s\n", cyan("dotsync integrate vercel --env "+env))
			fmt.Printf("    %s\n", cyan("dotsync integrate railway --env "+env))
			fmt.Println()
			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment scope: dev | staging | production | * (all)")
	cmd.Flags().StringVar(&nameFlag, "name", "", "human-readable label for this token")
	return cmd
}

// ── dotsync tokens list ──────────────────────────────────────────────────────

func tokensListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List service tokens for this project",
		Example: `  dotsync tokens list`,
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
			tokens, err := client.ListServiceTokens(projCfg.ProjectSlug)
			if err != nil {
				return fmt.Errorf("list tokens: %w", err)
			}

			fmt.Println()
			fmt.Printf("  Service Tokens — %s\n", bold(projCfg.ProjectSlug))
			fmt.Println()

			if len(tokens) == 0 {
				fmt.Println(dim("  No service tokens yet."))
				fmt.Println()
				fmt.Printf("  Create one with: %s\n", cyan("dotsync tokens create"))
				fmt.Println()
				return nil
			}

			rule := strings.Repeat("─", 70)
			fmt.Println("  " + rule)
			fmt.Printf("  %-24s  %-12s  %-20s  %s\n",
				bold("NAME"), bold("ENV"), bold("CREATED"), bold("LAST USED"))
			fmt.Println("  " + rule)

			for _, t := range tokens {
				name := str(t["name"])
				env := str(t["env"])
				id := str(t["id"])
				createdAt := parseTime(str(t["created_at"]))
				lastUsed := "never"
				if lu, ok := t["last_used_at"]; ok && lu != nil && lu != "" {
					lastUsed = parseTime(str(lu))
				}

				fmt.Printf("  %-24s  %-12s  %-20s  %s\n",
					truncate(name, 24), env, createdAt, lastUsed)
				fmt.Printf("  %s  id: %s\n", strings.Repeat(" ", 24+2+12+2), dim(id))
			}

			fmt.Println("  " + rule)
			fmt.Printf("\n  %d token(s). Revoke with: %s\n\n",
				len(tokens), cyan("dotsync tokens revoke <id>"))
			return nil
		},
	}
	return cmd
}

// ── dotsync tokens revoke ────────────────────────────────────────────────────

func tokensRevokeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "revoke <token-id>",
		Short: "Revoke a service token",
		Long: `Immediately revokes a service token. Any CI/CD pipeline using
this token will start receiving 401 Unauthorized on the next request.

Get the token ID from: dotsync tokens list`,
		Args:    cobra.ExactArgs(1),
		Example: `  dotsync tokens revoke 3f2a1b4c-...`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireLogin()
			if err != nil {
				return err
			}
			projCfg, err := config.LoadProject()
			if err != nil {
				return err
			}

			tokenID := args[0]
			client := api.New(cfg)
			if err := client.RevokeServiceToken(projCfg.ProjectSlug, tokenID); err != nil {
				return fmt.Errorf("revoke token: %w", err)
			}

			fmt.Println()
			fmt.Println(ok(fmt.Sprintf("Token %s revoked.", bold(tokenID))))
			fmt.Println()
			fmt.Println("  Any CI/CD pipeline using this token will receive 401 Unauthorized.")
			fmt.Printf("  Run %s to see remaining tokens.\n", cyan("dotsync tokens list"))
			fmt.Println()
			return nil
		},
	}
	return cmd
}

// ── helpers ──────────────────────────────────────────────────────────────────

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func parseTime(s string) string {
	if s == "" {
		return "unknown"
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04:05Z", s)
		if err != nil {
			return s
		}
	}
	diff := time.Since(t)
	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		return fmt.Sprintf("%dm ago", int(diff.Minutes()))
	case diff < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(diff.Hours()))
	default:
		return t.Format("2006-01-02")
	}
}
