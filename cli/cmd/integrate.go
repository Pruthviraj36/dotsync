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
platforms. Each command creates a scoped service token for you
and prints a copy-paste snippet configured for your project.

Manage tokens with: dotsync tokens list / revoke`,
	}
	cmd.AddCommand(
		integrateGitHubActions(),
		integrateVercel(),
		integrateRailway(),
		integrateNetlify(),
		integrateDocker(),
		integrateShell(),
	)
	return cmd
}

// createToken creates a service token and returns the raw value.
// On failure it prints a warning and returns a placeholder so the snippet
// is still useful (user just needs to fill in the token manually).
func createToken(client *api.Client, projectSlug, env, platform string) string {
	name := platform + "-" + env
	token, err := client.CreateServiceToken(projectSlug, env, name)
	if err != nil {
		fmt.Println(warn("Could not auto-create service token: " + err.Error()))
		fmt.Println("  Create one manually with: " + cyan("dotsync tokens create --env "+env))
		fmt.Println()
		return "DOTSYNC_TOKEN_PLACEHOLDER"
	}
	fmt.Println(ok("Service token created for " + bold(platform) + " (" + env + ")."))
	fmt.Println()
	fmt.Printf("  %s  %s\n", bold("Token:"), cyan(token))
	fmt.Println()
	fmt.Println(warn("Store this token now — it cannot be retrieved after this screen."))
	fmt.Println()
	return token
}

// ── GitHub Actions ────────────────────────────────────────────────────────────

func integrateGitHubActions() *cobra.Command {
	var envFlag string

	cmd := &cobra.Command{
		Use:   "github-actions",
		Short: "GitHub Actions workflow snippet",
		Long: `Creates a scoped service token and generates a GitHub Actions
workflow step. Add the token to your repo secrets as DOTSYNC_TOKEN,
then paste the YAML into .github/workflows/deploy.yml.`,
		Example: `  dotsync integrate github-actions
  dotsync integrate github-actions --env production`,
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
			token := createToken(client, projCfg.ProjectSlug, env, "github-actions")

			fmt.Printf("  Add this secret to your repo:\n")
			fmt.Printf("  GitHub → Settings → Secrets and variables → Actions → New secret\n")
			fmt.Printf("  Name: DOTSYNC_TOKEN   Value: %s\n\n", cyan(token))

			snippet := fmt.Sprintf(`# .github/workflows/deploy.yml
# Requires: DOTSYNC_TOKEN set in GitHub repo secrets

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
        run: curl -fsSL %s/install.sh | sh

      - name: Pull secrets
        env:
          DOTSYNC_TOKEN: ${{ secrets.DOTSYNC_TOKEN }}
          DOTSYNC_SERVER: %s
        run: dotsync pull --env %s --force

      - name: Build
        run: npm run build   # replace with your build command`,
				serverURL(cfg), serverURL(cfg), env)

			printSnippet("GitHub Actions", snippet)
			return nil
		},
	}
	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	return cmd
}

// ── Vercel ────────────────────────────────────────────────────────────────────

func integrateVercel() *cobra.Command {
	var envFlag string

	cmd := &cobra.Command{
		Use:   "vercel",
		Short: "Vercel environment variable sync",
		Long: `Creates a service token and generates a shell script that syncs
your DotSync secrets to Vercel's environment variable store.
Run this script locally after any secret change — Vercel then
injects them into your builds and serverless functions automatically.`,
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

			client := api.New(cfg)
			_ = createToken(client, projCfg.ProjectSlug, env, "vercel")

			// Count keys in local .env for context
			keyCount := 0
			if data, err := os.ReadFile(".env"); err == nil {
				keyCount = len(cliCrypto.ParseEnvFile(string(data)))
			}

			vercelTarget := envToVercelTarget(env)

			snippet := fmt.Sprintf(`# Sync DotSync secrets to Vercel
# Prerequisites: npm i -g vercel && vercel login && vercel link

# 1. Pull latest secrets
dotsync pull --env %s --force

# 2. Push each secret to Vercel (%s environment)
while IFS='=' read -r key value; do
  [[ "$key" =~ ^[[:space:]]*# ]] && continue  # skip comments
  [[ -z "$key" ]] && continue                  # skip blank lines
  echo "$value" | vercel env add "$key" %s --force
done < .env

# That's it. Vercel injects these into every build and function automatically.
# Re-run this script after any dotsync push.

# Alternative — zero-disk injection (nothing written, secrets in env vars only):
# dotsync run --env %s -- vercel build`, env, vercelTarget, vercelTarget, env)

			printSnippet("Vercel", snippet)

			if keyCount > 0 {
				fmt.Printf("  %d keys in your local .env ready to sync.\n\n", keyCount)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	return cmd
}

// ── Railway ───────────────────────────────────────────────────────────────────

func integrateRailway() *cobra.Command {
	var envFlag string

	cmd := &cobra.Command{
		Use:   "railway",
		Short: "Railway deployment snippet",
		Long: `Creates a service token and generates a Railway integration script
that syncs your DotSync secrets to Railway environment variables.`,
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

			client := api.New(cfg)
			_ = createToken(client, projCfg.ProjectSlug, env, "railway")

			snippet := fmt.Sprintf(`# Sync DotSync secrets to Railway
# Prerequisites: npm i -g @railway/cli && railway login && railway link

# 1. Pull latest secrets
dotsync pull --env %s --force

# 2. Push each var to Railway
while IFS='=' read -r key value; do
  [[ "$key" =~ ^[[:space:]]*# ]] && continue
  [[ -z "$key" ]] && continue
  railway variables set "$key=$value"
done < .env

# Alternative — zero-disk: inject secrets and deploy in one command
# dotsync run --env %s -- railway up`, env, env)

			printSnippet("Railway", snippet)
			fmt.Printf("  Zero-disk deploy: %s\n\n", cyan(fmt.Sprintf("dotsync run --env %s -- railway up", env)))
			return nil
		},
	}
	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	return cmd
}

// ── Netlify ───────────────────────────────────────────────────────────────────

func integrateNetlify() *cobra.Command {
	var envFlag string

	cmd := &cobra.Command{
		Use:   "netlify",
		Short: "Netlify build environment snippet",
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
			_ = createToken(client, projCfg.ProjectSlug, env, "netlify")

			snippet := fmt.Sprintf(`# Sync DotSync secrets to Netlify
# Prerequisites: npm i -g netlify-cli && netlify login && netlify link

# 1. Pull latest secrets
dotsync pull --env %s --force

# 2. Push each var to Netlify
while IFS='=' read -r key value; do
  [[ "$key" =~ ^[[:space:]]*# ]] && continue
  [[ -z "$key" ]] && continue
  netlify env:set "$key" "$value"
done < .env

# For Netlify build plugins, add to netlify.toml:
# [build.environment]
#   DOTSYNC_PROJECT = "%s"
#   DOTSYNC_ENV     = "%s"
# Then add DOTSYNC_TOKEN to Netlify → Site settings → Environment variables.`, env, projCfg.ProjectSlug, env)

			printSnippet("Netlify", snippet)
			return nil
		},
	}
	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	return cmd
}

// ── Docker ────────────────────────────────────────────────────────────────────

func integrateDocker() *cobra.Command {
	var envFlag string

	cmd := &cobra.Command{
		Use:   "docker",
		Short: "Docker / Docker Compose snippet",
		Long: `Generates Docker and Docker Compose configurations that inject
secrets at container runtime — nothing is baked into the image.`,
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
			_ = createToken(client, projCfg.ProjectSlug, env, "docker")

			snippet := fmt.Sprintf(`# Docker — inject secrets at runtime (never baked into image)

# Option 1: pull .env then pass as --env-file
dotsync pull --env %s --force
docker run --env-file .env your-image

# Option 2: zero-disk — secrets injected into process, never written
dotsync run --env %s -- docker run your-image

# Option 3: Docker Compose — reference your .env
# docker-compose.yml:
#   services:
#     app:
#       image: your-image
#       env_file: [.env]   # generate with: dotsync pull --env %s
#
# docker-compose up

# Option 4: Multi-stage build (secrets only at runtime, never at build time)
# FROM node:20 AS builder
# RUN npm ci && npm run build         # no secrets needed here
# FROM node:20-slim
# COPY --from=builder /app/dist ./dist
# CMD ["node", "server.js"]           # inject via --env-file at docker run`, env, env, env)

			printSnippet("Docker", snippet)
			fmt.Printf("  Recommended: %s\n\n",
				cyan(fmt.Sprintf("dotsync run --env %s -- docker run your-image", env)))
			return nil
		},
	}
	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	return cmd
}

// ── Shell ─────────────────────────────────────────────────────────────────────

func integrateShell() *cobra.Command {
	var envFlag string
	var shellFlag string

	cmd := &cobra.Command{
		Use:   "shell",
		Short: "Shell export snippet (bash/zsh/fish)",
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
			_ = createToken(client, projCfg.ProjectSlug, env, "shell")

			var snippet string
			switch strings.ToLower(shellFlag) {
			case "fish":
				snippet = fmt.Sprintf(`# Fish shell — export DotSync secrets into current session

# Option 1: subshell with secrets loaded (recommended)
dotsync run --env %s -- fish

# Option 2: pull and source
dotsync pull --env %s --force
for line in (cat .env)
  string match -qr '^#' $line; and continue
  set -gx (string split -m 1 '=' $line)
end`, env, env)
			default:
				snippet = fmt.Sprintf(`# Bash / Zsh — export DotSync secrets into current shell

# Option 1: subshell with secrets loaded (recommended — no disk writes)
dotsync run --env %s -- bash

# Option 2: pull and source (writes .env to disk)
dotsync pull --env %s --force
set -a; source .env; set +a

# Option 3: eval for current shell (no subshell, no disk)
eval "$(dotsync run --env %s -- env | grep -E '^[A-Z_]+=' | sed 's/^/export /')"`, env, env, env)
			}

			printSnippet("Shell ("+shellFlag+")", snippet)
			return nil
		},
	}
	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	cmd.Flags().StringVar(&shellFlag, "shell", "bash", "shell type: bash | zsh | fish")
	return cmd
}

// ── helpers ───────────────────────────────────────────────────────────────────

func printSnippet(platform, snippet string) {
	rule := strings.Repeat("─", 64)
	fmt.Printf("  %s — %s\n", bold("Integration"), bold(platform))
	fmt.Println("  " + rule)
	fmt.Println()
	fmt.Println(snippet)
	fmt.Println()
	fmt.Println("  " + rule)
	fmt.Println()
}

func serverURL(cfg *config.GlobalConfig) string {
	if cfg.ServerURL != "" {
		return cfg.ServerURL
	}
	return "https://dotsync.onrender.com"
}

func envToVercelTarget(env string) string {
	switch env {
	case "production":
		return "production"
	case "dev", "development":
		return "development"
	default:
		return "preview"
	}
}
