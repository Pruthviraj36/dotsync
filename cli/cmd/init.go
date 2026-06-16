package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
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

			// Show existing projects
			projects, err := client.ListProjects()
			if err == nil && len(projects) > 0 {
				fmt.Println("Your projects:")
				for _, p := range projects {
					fmt.Printf("  • %s (slug: %s)\n", p["name"], p["slug"])
				}
				fmt.Println()
			}

			if create {
				return createNewProject(client, reader)
			}

			fmt.Print("Project slug (or type 'new' to create): ")
			slug, _ := reader.ReadString('\n')
			slug = strings.TrimSpace(slug)

			if slug == "new" {
				return createNewProject(client, reader)
			}

			if slug == "" {
				return fmt.Errorf("slug cannot be empty")
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
				ProjectSlug: slug,
				DefaultEnv:  env,
			}
			if err := config.SaveProject(projCfg); err != nil {
				return fmt.Errorf("save project config: %w", err)
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

func createNewProject(client *api.Client, reader *bufio.Reader) error {
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

	fmt.Print("⏳ Creating project...")
	proj, err := client.CreateProject(name, slug, desc)
	if err != nil {
		fmt.Println(" ❌")
		return err
	}
	fmt.Println(" ✅")

	projCfg := &config.ProjectConfig{
		ProjectSlug: proj["slug"].(string),
		DefaultEnv:  "dev",
	}
	if err := config.SaveProject(projCfg); err != nil {
		return fmt.Errorf("save project config: %w", err)
	}

	ensureGitignore()

	fmt.Printf("\n✅ Project '%s' created and linked!\n", name)
	fmt.Println("\n  3 environments auto-created: dev, staging, production")
	fmt.Println("  Run: dotsync push")
	fmt.Println()

	return nil
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
