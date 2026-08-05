package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
	cliCrypto "github.com/Pruthviraj36/dotsync/cli/crypto"
)

func integrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "integrate",
		Short: "Generate integration snippets for CI/CD platforms",
		Long: `Generate ready-to-use integration snippets for popular CI/CD
platforms and deployment services. Secrets are injected at runtime
and never committed to your repository.`,
	}

	cmd.AddCommand(
		integrateGitHubActions(),
		integrateVercel(),
		integrateRailway(),
		integrateNetlify(),
		integrateDocker(),
		integrateEnvExport(),
	)

	return cmd
}

// ─── GitHub Actions ───────────────────────────────────────────────────────────

func integrateGitHubActions() *cobra.Command {
	var envFlag string
	var serviceTokenFlag string

	cmd := &cobra.Command{
		Use:   "github-actions",
		Short: "GitHub Actions workflow snippet",
		Long: `Generates a GitHub Actions workflow step that injects your
DotSync secrets as environment variables at runtime using a
service token. No secrets are stored in your repository.`,
		Example: `  dotsync integrate github-actions
  dotsync integrate github-actions --env production
  dotsync integrate github-actions --token <service-token>`,
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

			token := serviceTokenFlag
			if token == "" {
				// Generate a service token if not provided
				client := api.New(cfg)
				token, err = client.CreateServiceToken(projCfg.ProjectSlug, env)
				if err != nil {
					token = "${{ secrets.DOTSYNC_TOKEN }}"
					fmt.Println(warn("Could not create service token — using placeholder."))
					fmt.Println("  Set DOTSYNC_TOKEN in your GitHub repo secrets.")
					fmt.Println()
				} else {
					fmt.Println(ok("Service token created."))
					fmt.Printf("  Add this to your GitHub repo secrets as %s:\n", bold("DOTSYNC_TOKEN"))
					fmt.Printf("  %s\n\n", cyan(token))
					token = "${{ secrets.DOTSYNC_TOKEN }}"
				}
			}

			snippet := fmt.Sprintf(`# .github/workflows/deploy.yml
# Add DOTSYNC_TOKEN to your GitHub repo secrets (Settings > Secrets > Actions)

name: Deploy

on:
  push:
    branches: [main]

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Install DotSync
        run: curl -fsSL https://dotsync.onrender.com/install.sh | sh

      - name: Pull secrets
        run: dotsync pull --env %s --force
        env:
          DOTSYNC_TOKEN: %s
          DOTSYNC_PROJECT: %s

      - name: Your build step
        run: npm run build  # or your actual build command
        # .env is now populated with your secrets
`, env, token, projCfg.ProjectSlug)

			printSnippet("GitHub Actions", snippet)
			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	cmd.Flags().StringVar(&serviceTokenFlag, "token", "", "use existing service token")
	return cmd
}

// ─── Vercel ───────────────────────────────────────────────────────────────────

func integrateVercel() *cobra.Command {
	var envFlag string

	cmd := &cobra.Command{
		Use:   "vercel",
		Short: "Vercel environment variable snippet",
		Long: `Exports your current .env as Vercel environment variables using
the Vercel CLI. Reads your local decrypted .env and syncs each
key to your Vercel project.`,
		Example: `  dotsync integrate vercel
  dotsync integrate vercel --env production`,
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

			// Try to read and show which keys would be synced
			data, readErr := os.ReadFile(".env")
			keyCount := 0
			if readErr == nil {
				keys := cliCrypto.ParseEnvFile(string(data))
				keyCount = len(keys)
			}

			// Map DotSync env names to Vercel env targets
			vercelTarget := "preview"
			switch env {
			case "production":
				vercelTarget = "production"
			case "dev", "development":
				vercelTarget = "development"
			default:
				vercelTarget = "preview"
			}

			_ = cfg // used via requireLogin

			snippet := fmt.Sprintf(`# Sync DotSync secrets to Vercel
# Run this after: dotsync pull --env %s

# One-time setup: install Vercel CLI and login
# npm i -g vercel && vercel login

# Push each secret from your .env to Vercel (%s environment)
while IFS='=' read -r key value; do
  # Skip comments and empty lines
  [[ "$key" =~ ^[[:space:]]*# ]] && continue
  [[ -z "$key" ]] && continue
  echo "$value" | vercel env add "$key" %s --force
done < .env

# Or use vercel env pull to go the other direction:
# vercel env pull .env.local`, env, vercelTarget, vercelTarget)

			printSnippet("Vercel", snippet)

			if keyCount > 0 {
				fmt.Printf("  %d keys in your local .env ready to sync.\n\n", keyCount)
			}

			fmt.Println("  Alternatively, use the one-liner:")
			fmt.Println()
			fmt.Printf("    %s\n\n", cyan(fmt.Sprintf(
				"dotsync run --env %s -- vercel env push", env)))

			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	return cmd
}

// ─── Railway ──────────────────────────────────────────────────────────────────

func integrateRailway() *cobra.Command {
	var envFlag string

	cmd := &cobra.Command{
		Use:   "railway",
		Short: "Railway deployment snippet",
		Long: `Generates a Railway deployment configuration that syncs
your DotSync secrets to Railway environment variables.`,
		Example: `  dotsync integrate railway
  dotsync integrate railway --env production`,
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

			_ = cfg

			snippet := fmt.Sprintf(`# Sync DotSync secrets to Railway
# Requires Railway CLI: npm i -g @railway/cli && railway login

# Pull your secrets locally first
dotsync pull --env %s --force

# Push each var to Railway
while IFS='=' read -r key value; do
  [[ "$key" =~ ^[[:space:]]*# ]] && continue
  [[ -z "$key" ]] && continue
  railway variables set "$key=$value"
done < .env

# Or run your process with secrets injected (nothing hits disk):
dotsync run --env %s -- railway up`, env, env)

			printSnippet("Railway", snippet)

			fmt.Println("  Alternatively, run your Railway deploy with live secrets:")
			fmt.Println()
			fmt.Printf("    %s\n\n", cyan(fmt.Sprintf(
				"dotsync run --env %s -- railway up", env)))

			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	return cmd
}

// ─── Netlify ──────────────────────────────────────────────────────────────────

func integrateNetlify() *cobra.Command {
	var envFlag string

	cmd := &cobra.Command{
		Use:   "netlify",
		Short: "Netlify build environment snippet",
		Long: `Generates a Netlify configuration that injects your DotSync
secrets as Netlify environment variables for builds and functions.`,
		Example: `  dotsync integrate netlify
  dotsync integrate netlify --env production`,
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

			_ = cfg

			snippet := fmt.Sprintf(`# Sync DotSync secrets to Netlify
# Requires Netlify CLI: npm i -g netlify-cli && netlify login

# Pull your secrets first
dotsync pull --env %s --force

# Push each var to Netlify (site must be linked: netlify link)
while IFS='=' read -r key value; do
  [[ "$key" =~ ^[[:space:]]*# ]] && continue
  [[ -z "$key" ]] && continue
  netlify env:set "$key" "$value"
done < .env

# For Netlify build plugins, add this to netlify.toml:
# [build.environment]
#   DOTSYNC_PROJECT = "%s"
#   DOTSYNC_ENV = "%s"
# Then add DOTSYNC_TOKEN to your Netlify environment via the dashboard.`, env, projCfg.ProjectSlug, env)

			printSnippet("Netlify", snippet)
			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	return cmd
}

// ─── Docker ───────────────────────────────────────────────────────────────────

func integrateDocker() *cobra.Command {
	var envFlag string

	cmd := &cobra.Command{
		Use:   "docker",
		Short: "Docker / Docker Compose snippet",
		Long: `Generates Docker and Docker Compose configurations that
inject your DotSync secrets at container runtime without
baking them into your image.`,
		Example: `  dotsync integrate docker
  dotsync integrate docker --env production`,
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

			_ = cfg

			snippet := fmt.Sprintf(`# Docker — inject secrets at runtime (never baked into image)

# Option 1: Pass .env file directly (pull first)
dotsync pull --env %s --force
docker run --env-file .env your-image

# Option 2: Use dotsync run (secrets never touch disk)
dotsync run --env %s -- docker run your-image

# Option 3: Docker Compose — reference your .env file
# docker-compose.yml:
#
# services:
#   app:
#     image: your-image
#     env_file:
#       - .env   # generated by: dotsync pull --env %s
#
# Run:
#   dotsync pull --env %s --force && docker compose up

# Option 4: Multi-stage build — secrets only at runtime, not build time
# FROM node:20 AS builder
# # Do NOT COPY .env here
# RUN npm ci && npm run build
#
# FROM node:20-slim
# COPY --from=builder /app/dist ./dist
# # Inject secrets at 'docker run' time using --env-file
`, env, env, env, env)

			printSnippet("Docker", snippet)

			fmt.Println("  The zero-disk approach (no .env ever written):")
			fmt.Println()
			fmt.Printf("    %s\n\n", cyan(fmt.Sprintf(
				"dotsync run --env %s -- docker run your-image", env)))

			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	return cmd
}

// ─── env export ───────────────────────────────────────────────────────────────

func integrateEnvExport() *cobra.Command {
	var envFlag string
	var shellFlag string

	cmd := &cobra.Command{
		Use:   "shell",
		Short: "Shell export snippet (bash/zsh/fish)",
		Long: `Generates a shell snippet to export your DotSync secrets as
environment variables in the current shell session.
Secrets are decrypted in-process and exported — nothing is written to disk.`,
		Example: `  dotsync integrate shell
  dotsync integrate shell --env production
  dotsync integrate shell --shell fish`,
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

			_ = cfg

			var snippet string
			switch strings.ToLower(shellFlag) {
			case "fish":
				snippet = fmt.Sprintf(`# Fish shell — export DotSync secrets into current session
# Add to ~/.config/fish/config.fish or run manually

# Option 1: Use dotsync run (recommended — no disk writes)
dotsync run --env %s -- fish

# Option 2: Pull and source
dotsync pull --env %s --force
for line in (cat .env)
  if not string match -q '#*' $line
    set -gx (string split -m 1 '=' $line)
  end
end`, env, env)

			default: // bash/zsh
				snippet = fmt.Sprintf(`# Bash/Zsh — export DotSync secrets into current shell session

# Option 1: Use dotsync run (recommended — secrets never hit disk)
dotsync run --env %s -- bash  # opens a subshell with secrets loaded

# Option 2: Pull and source (writes .env to disk)
dotsync pull --env %s --force
set -a && source .env && set +a

# Option 3: eval for current shell (no subshell, no disk)
eval "$(dotsync run --env %s -- env | grep -E '^[A-Z_]+=' | sed 's/^/export /')"`, env, env, env)
			}

			printSnippet("Shell ("+shellFlag+")", snippet)
			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	cmd.Flags().StringVar(&shellFlag, "shell", "bash", "shell type (bash|zsh|fish)")
	return cmd
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func printSnippet(platform, snippet string) {
	rule := strings.Repeat("─", 60)
	fmt.Println()
	fmt.Printf("%s  Integration: %s\n", bold(""), bold(platform))
	fmt.Println(rule)
	fmt.Println()
	fmt.Println(snippet)
	fmt.Println()
	fmt.Println(rule)
	fmt.Println()
}
