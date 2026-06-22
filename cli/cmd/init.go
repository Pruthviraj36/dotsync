package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func initCmd() *cobra.Command {
	var create bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Link this folder to a DotSync project",
		Long: `Creates or links this directory to a DotSync project.

A .dotsync.json file is created in the current directory — commit
this file but NOT your .env. Add .env to your .gitignore.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireLogin()
			if err != nil {
				return err
			}

			client := api.New(cfg)
			reader := bufio.NewReader(os.Stdin)

			fmt.Println()
			fmt.Println("🔗 Link to a DotSync project")
			fmt.Println("────────────────────────────")
			fmt.Println()

			// Show existing projects — listErr intentionally kept separate;
			// a failure here should not block the user from proceeding.
			projects, listErr := client.ListProjects()
			if listErr == nil && len(projects) > 0 {
				fmt.Println("Your projects:")
				for _, p := range projects {
					fmt.Printf("  • %s (slug: %s)\n", p["name"], p["slug"])
				}
				fmt.Println()
			}

			if create {
				return createNewProject(client, cfg, reader)
			}

			fmt.Print("Project slug (or type 'new' to create): ")
			slug, _ := reader.ReadString('\n')
			slug = strings.TrimSpace(slug)

			if slug == "new" {
				return createNewProject(client, cfg, reader)
			}

			if slug == "" {
				return fmt.Errorf("slug cannot be empty")
			}

			// Only validate against the project list if we actually fetched one.
			// If ListProjects failed (server unreachable, token issue, etc.) we
			// let the user proceed — a wrong slug will surface on push/pull instead.
			if listErr == nil {
				var matched bool
				for _, p := range projects {
					if p["slug"] == slug {
						matched = true
						break
					}
				}
				if !matched {
					return fmt.Errorf(
						"project '%s' not found in your account\n"+
							"  Run: dotsync init new   — to create it\n"+
							"  Run: dotsync init       — to see your projects",
						slug,
					)
				}
			}

			password, err := readPassword("Project Password (for end-to-end encryption): ")
			if err != nil {
				return err
			}

			fmt.Print("Default environment [dev]: ")
			env, _ := reader.ReadString('\n')
			env = strings.TrimSpace(env)
			if env == "" {
				env = "dev"
			}
			if env != "dev" && env != "staging" && env != "production" {
				return fmt.Errorf("environment must be one of: dev, staging, production")
			}

			projCfg := &config.ProjectConfig{
				ProjectSlug:     slug,
				DefaultEnv:      env,
			}
			if err := config.SaveProject(projCfg); err != nil {
				return fmt.Errorf("save project config: %w", err)
			}

			if cfg.ProjectPasswords == nil {
				cfg.ProjectPasswords = make(map[string]string)
			}
			cfg.ProjectPasswords[slug] = password
			if err := config.SaveGlobal(cfg); err != nil {
				return fmt.Errorf("save global config: %w", err)
			}

			ensureGitignore()

			fmt.Println()
			fmt.Printf("✅ Linked to project '%s' (env: %s)\n", slug, env)
			fmt.Println()
			fmt.Println("  dotsync push    # upload your .env")
			fmt.Println("  dotsync pull    # download latest .env")
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().BoolVar(&create, "new", false, "create a new project")
	return cmd
}

func createNewProject(client *api.Client, cfg *config.GlobalConfig, reader *bufio.Reader) error {
	fmt.Print("Project name: ")
	name, _ := reader.ReadString('\n')
	name = strings.TrimSpace(name)

	fmt.Print("Project slug (lowercase, hyphens only): ")
	slug, _ := reader.ReadString('\n')
	slug = strings.TrimSpace(slug)

	fmt.Print("Description (optional): ")
	desc, _ := reader.ReadString('\n')
	desc = strings.TrimSpace(desc)

	if name == "" || slug == "" {
		return fmt.Errorf("name and slug are required")
	}

	password, err := readPassword("Create Project Password (for end-to-end encryption): ")
	if err != nil {
		return err
	}

	fmt.Print("⏳ Creating project...")
	proj, err := client.CreateProject(name, slug, desc)
	if err != nil {
		fmt.Println(" ❌")
		return err
	}
	fmt.Println(" ✅")

	projCfg := &config.ProjectConfig{
		ProjectSlug:     proj["slug"].(string),
		DefaultEnv:      "dev",
	}
	if err := config.SaveProject(projCfg); err != nil {
		return fmt.Errorf("save project config: %w", err)
	}

	if cfg.ProjectPasswords == nil {
		cfg.ProjectPasswords = make(map[string]string)
	}
	cfg.ProjectPasswords[proj["slug"].(string)] = password
	if err := config.SaveGlobal(cfg); err != nil {
		return fmt.Errorf("save global config: %w", err)
	}

	ensureGitignore()

	fmt.Printf("\n✅ Project '%s' created and linked!\n", name)
	fmt.Println("\n  3 environments auto-created: dev, staging, production")
	fmt.Println("  Run: dotsync push")
	fmt.Println()

	return nil
}

// readPassword reads a password from stdin with echo disabled.
// Falls back to plain readline when stdin is not a real terminal (CI, pipes).
func readPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		pw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println() // restore cursor to new line after hidden input
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		if strings.TrimSpace(string(pw)) == "" {
			return "", fmt.Errorf("password cannot be empty")
		}
		return string(pw), nil
	}
	// Non-terminal fallback
	r := bufio.NewReader(os.Stdin)
	pw, _ := r.ReadString('\n')
	pw = strings.TrimSpace(pw)
	if pw == "" {
		return "", fmt.Errorf("password cannot be empty")
	}
	return pw, nil
}

// ensureGitignore adds .env to .gitignore if not already present.
func ensureGitignore() {
	data, _ := os.ReadFile(".gitignore")
	content := string(data)

	var toAdd []string
	if !strings.Contains(content, ".env") {
		toAdd = append(toAdd, ".env")
	}
	if !strings.Contains(content, ".env.local") {
		toAdd = append(toAdd, ".env.local")
	}

	if len(toAdd) == 0 {
		return
	}

	f, err := os.OpenFile(".gitignore", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	if len(data) > 0 && !strings.HasSuffix(content, "\n") {
		f.WriteString("\n")
	}
	f.WriteString("\n# DotSync — never commit secrets\n")
	for _, entry := range toAdd {
		f.WriteString(entry + "\n")
	}

	fmt.Println("  📝 Added .env to .gitignore")
}

// requireLogin loads config and validates login state.
func requireLogin() (*config.GlobalConfig, error) {
	cfg, err := config.LoadGlobal()
	if err != nil {
		return nil, err
	}
	if !config.IsLoggedIn(cfg) {
		return nil, fmt.Errorf("not logged in — run: dotsync login")
	}
	return cfg, nil
}
