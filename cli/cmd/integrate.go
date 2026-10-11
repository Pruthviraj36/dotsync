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
		Use:   "integrate <platform>",
		Short: "Set up CI/CD integration in seconds",
		Long: `Creates a scoped service token and shows exactly what to do next.
Nothing is auto-configured — you copy and paste two things.

Platforms: github-actions  vercel  railway  netlify  docker  shell`,
		Example: `  dotsync integrate github-actions
  dotsync integrate vercel --env production
  dotsync integrate docker`,
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

// makeToken creates a service token and prints just what the user needs.
// Returns the raw token string.
func makeToken(client *api.Client, projectSlug, env, platform string) (string, error) {
	name := platform + "-" + env
	token, err := client.CreateServiceToken(projectSlug, env, name)
	if err != nil {
		return "", fmt.Errorf("create token: %w", err)
	}
	return token, nil
}

func showToken(token, platform, env string) {
	banner(fmt.Sprintf("Integration · %s (%s)", platform, env))
	blank()
	fmt.Printf(" TOKEN: %s\n", bold(token))
	blank()
	fmt.Println(warn("Add this token to your secret store as DOTSYNC_TOKEN. It is displayed only once."))
	blank()
}

// ── GitHub Actions ────────────────────────────────────────────────────────────

func integrateGitHubActions() *cobra.Command {
	var envFlag string

	cmd := &cobra.Command{
		Use:   "github-actions",
		Short: "GitHub Actions",
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
			token, err := makeToken(client, projCfg.ProjectSlug, env, "github-actions")
			if err != nil {
				return err
			}

			showToken(token, "GitHub Actions", env)

			fmt.Println("Setup Steps:")
			fmt.Println("  1. In GitHub Repository -> Settings -> Secrets and variables -> Actions")
			fmt.Println("     Add secret: DOTSYNC_TOKEN = <token above>")
			blank()
			fmt.Println("  2. In your .github/workflows/*.yml step:")
			fmt.Println("     - name: Pull secrets")
			fmt.Printf("       run: dotsync pull --env %s --force\n", env)
			fmt.Println("       env:")
			fmt.Println("         DOTSYNC_TOKEN: ${{ secrets.DOTSYNC_TOKEN }}")
			fmt.Printf("         DOTSYNC_SERVER: %s\n", serverURL(cfg))
			blank()
			complete("")
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
		Short: "Vercel",
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

			// Count local keys for context
			keyCount := 0
			if data, err := os.ReadFile(".env"); err == nil {
				keyCount = len(cliCrypto.ParseEnvFile(string(data)))
			}

			client := api.New(cfg)
			token, err := makeToken(client, projCfg.ProjectSlug, env, "vercel")
			if err != nil {
				return err
			}

			showToken(token, "Vercel", env)

			fmt.Println("Setup Steps:")
			fmt.Printf("  1. Pull secrets locally: dotsync pull --env %s --force\n", env)
			fmt.Printf("  2. Push to Vercel (%d keys):\n", keyCount)
			fmt.Printf("     cat .env | while IFS='=' read -r k v; do vercel env add \"$k\" %s <<< \"$v\"; done\n", vercelTarget(env))
			blank()
			hint("Vercel injects these secrets into build & deployment runs automatically.")
			hint("Re-run step 2 whenever you update secrets.")
			complete("")
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
		Short: "Railway",
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
			token, err := makeToken(client, projCfg.ProjectSlug, env, "railway")
			if err != nil {
				return err
			}

			showToken(token, "Railway", env)

			fmt.Println("Setup Steps:")
			fmt.Printf("  1. Pull secrets: dotsync pull --env %s --force\n", env)
			fmt.Println("  2. Push to Railway:")
			fmt.Println("     cat .env | while IFS='=' read -r k v; do railway variables set \"$k=$v\"; done")
			blank()
			hint(fmt.Sprintf("Or deploy directly without touching disk: dotsync run --env %s -- railway up", env))
			complete("")
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
		Short: "Netlify",
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
			token, err := makeToken(client, projCfg.ProjectSlug, env, "netlify")
			if err != nil {
				return err
			}

			showToken(token, "Netlify", env)

			fmt.Println("Setup Steps:")
			fmt.Printf("  1. Pull secrets: dotsync pull --env %s --force\n", env)
			fmt.Println("  2. Push to Netlify:")
			fmt.Println("     cat .env | while IFS='=' read -r k v; do netlify env:set \"$k\" \"$v\"; done")
			blank()
			complete("")
			return nil
		},
	}
	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	return cmd
}

// ── Docker / Podman ───────────────────────────────────────────────────────────

func integrateDocker() *cobra.Command {
	var envFlag string

	cmd := &cobra.Command{
		Use:     "docker",
		Aliases: []string{"podman"},
		Short:   "Docker or Podman",
		Long:    `Works with both Docker and Podman — they use the same CLI interface.`,
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

			// Detect whether podman or docker is available
			runtime := "docker"
			if _, err := exec_lookpath("podman"); err == nil {
				if _, err2 := exec_lookpath("docker"); err2 != nil {
					runtime = "podman"
				}
			}

			client := api.New(cfg)
			token, err := makeToken(client, projCfg.ProjectSlug, env, runtime)
			if err != nil {
				return err
			}

			showToken(token, "Docker / Podman", env)

			fmt.Println("Integration Modes:")
			fmt.Println("  A. In-memory injection (Zero-disk, recommended):")
			fmt.Printf("     dotsync run --env %s -- %s run your-image\n\n", env, runtime)
			fmt.Println("  B. Pull to .env and mount:")
			fmt.Printf("     dotsync pull --env %s --force\n", env)
			fmt.Printf("     %s run --env-file .env your-image\n\n", runtime)
			fmt.Println("  C. Docker Compose:")
			fmt.Printf("     dotsync pull --env %s --force && %s compose up\n", env, runtime)
			blank()
			complete("")
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
		Short: "Bash / Zsh / Fish",
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
			token, err := makeToken(client, projCfg.ProjectSlug, env, "shell")
			if err != nil {
				return err
			}

			showToken(token, "Shell ("+shellFlag+")", env)

			fmt.Println("Integration Modes:")
			switch strings.ToLower(shellFlag) {
			case "fish":
				fmt.Println("  A. Subshell (secrets in memory only):")
				fmt.Printf("     dotsync run --env %s -- fish\n\n", env)
				fmt.Println("  B. Export into current session:")
				fmt.Printf("     dotsync pull --env %s --force\n", env)
				fmt.Println("     for line in (cat .env); set -gx (string split -m1 '=' $line); end")
			default:
				fmt.Println("  A. Subshell (secrets in memory only, recommended):")
				fmt.Printf("     dotsync run --env %s -- bash\n\n", env)
				fmt.Println("  B. Export into current shell:")
				fmt.Printf("     dotsync pull --env %s --force && source .env\n", env)
			}
			blank()
			complete("")
			return nil
		},
	}
	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	cmd.Flags().StringVar(&shellFlag, "shell", "bash", "shell type: bash|zsh|fish")
	return cmd
}

// ── helpers ───────────────────────────────────────────────────────────────────

func serverURL(cfg *config.GlobalConfig) string {
	return cfg.ServerURL
}

func vercelTarget(env string) string {
	switch env {
	case "production":
		return "production"
	case "dev", "development":
		return "development"
	default:
		return "preview"
	}
}

// exec_lookpath is a thin wrapper so we don't import os/exec at package level.
func exec_lookpath(name string) (string, error) {
	// Use PATH lookup manually to avoid import
	paths := strings.Split(os.Getenv("PATH"), ":")
	for _, dir := range paths {
		full := dir + "/" + name
		if _, err := os.Stat(full); err == nil {
			return full, nil
		}
	}
	return "", fmt.Errorf("not found: %s", name)
}
