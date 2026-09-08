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
	sectionTitle("Service token · save it now")
	item(cyan(token), "shown once; add it to your CI/CD secret store as DOTSYNC_TOKEN")
	blank()
	kv("platform", platform)
	kv("environment", env)
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

			fmt.Println(bold("  Two steps:"))
			blank()
			fmt.Printf("  %s  Go to your repo → Settings → Secrets → Actions\n", dim("1."))
			fmt.Printf("       Add a secret named %s with the token above\n", cyan("DOTSYNC_TOKEN"))
			blank()
			fmt.Printf("  %s  Add this to your workflow:\n", dim("2."))
			blank()
			fmt.Printf("       %s\n", dim("- name: Pull secrets"))
			fmt.Printf("         %s\n", dim("run: dotsync pull --env "+env+" --force"))
			fmt.Printf("         %s\n", dim("env:"))
			fmt.Printf("           %s\n", dim("DOTSYNC_TOKEN: ${{ secrets.DOTSYNC_TOKEN }}"))
			fmt.Printf("           %s\n", dim("DOTSYNC_SERVER: "+serverURL(cfg)))
			blank()
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

			fmt.Println(bold("  Two steps:"))
			blank()
			fmt.Printf("  %s  Pull your secrets locally:\n", dim("1."))
			fmt.Printf("       %s\n", cyan("dotsync pull --env "+env+" --force"))
			blank()
			fmt.Printf("  %s  Push them to Vercel (%d keys):\n", dim("2."), keyCount)
			fmt.Printf("       %s\n", cyan("cat .env | while IFS='=' read k v; do vercel env add $k "+vercelTarget(env)+" <<< $v; done"))
			blank()
			hint("After this, Vercel injects them into every build automatically.")
			hint("Re-run step 2 whenever you update secrets.")
			blank()
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

			fmt.Println(bold("  Two steps:"))
			blank()
			fmt.Printf("  %s  Pull secrets:\n", dim("1."))
			fmt.Printf("       %s\n", cyan("dotsync pull --env "+env+" --force"))
			blank()
			fmt.Printf("  %s  Push to Railway:\n", dim("2."))
			fmt.Printf("       %s\n", cyan("cat .env | while IFS='=' read k v; do railway variables set \"$k=$v\"; done"))
			blank()
			hint("Or deploy directly without touching disk:")
			cmdHint("  dotsync run --env " + env + " -- railway up")
			blank()
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

			fmt.Println(bold("  Two steps:"))
			blank()
			fmt.Printf("  %s  Pull secrets:\n", dim("1."))
			fmt.Printf("       %s\n", cyan("dotsync pull --env "+env+" --force"))
			blank()
			fmt.Printf("  %s  Push to Netlify:\n", dim("2."))
			fmt.Printf("       %s\n", cyan("cat .env | while IFS='=' read k v; do netlify env:set \"$k\" \"$v\"; done"))
			blank()
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

			fmt.Println(bold("  Options (pick one):"))
			blank()
			fmt.Printf("  %s  %s %s\n", dim("A."), dim("Zero-disk — nothing written, secrets in memory only:"), "")
			fmt.Printf("       %s\n", cyan(fmt.Sprintf("dotsync run --env %s -- %s run your-image", env, runtime)))
			blank()
			fmt.Printf("  %s  %s\n", dim("B."), dim("Pull .env then pass as file:"))
			fmt.Printf("       %s\n", cyan("dotsync pull --env "+env+" --force"))
			fmt.Printf("       %s\n", cyan(runtime+" run --env-file .env your-image"))
			blank()
			fmt.Printf("  %s  %s\n", dim("C."), dim("Docker Compose:"))
			fmt.Printf("       %s\n", dim("# In docker-compose.yml → services → app → env_file: [.env]"))
			fmt.Printf("       %s\n", cyan("dotsync pull --env "+env+" --force && "+runtime+" compose up"))
			blank()
			hint("Option A is recommended — secrets never touch disk.")
			blank()
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

			fmt.Println(bold("  Options:"))
			blank()

			switch strings.ToLower(shellFlag) {
			case "fish":
				fmt.Printf("  %s  %s\n", dim("A."), dim("Subshell with secrets loaded:"))
				fmt.Printf("       %s\n", cyan("dotsync run --env "+env+" -- fish"))
				blank()
				fmt.Printf("  %s  %s\n", dim("B."), dim("Export into current session:"))
				fmt.Printf("       %s\n", cyan("dotsync pull --env "+env+" --force"))
				fmt.Printf("       %s\n", cyan("for line in (cat .env); set -gx (string split -m1 '=' $line); end"))
			default:
				fmt.Printf("  %s  %s\n", dim("A."), dim("Subshell with secrets (nothing written to disk):"))
				fmt.Printf("       %s\n", cyan("dotsync run --env "+env+" -- bash"))
				blank()
				fmt.Printf("  %s  %s\n", dim("B."), dim("Export into current shell:"))
				fmt.Printf("       %s\n", cyan("dotsync pull --env "+env+" --force && source .env"))
			}
			blank()
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
