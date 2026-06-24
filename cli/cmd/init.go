package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
)

func initCmd() *cobra.Command {
	var create bool
	var rotatePassword bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Link this folder to a DotSync project",
		Long: `Creates or links this directory to a DotSync project.

A .dotsync.json file is created in the current directory — commit
this file but NOT your .env. Add .env to your .gitignore.

On a new machine where .dotsync.json already exists (from git),
run 'dotsync init --rotate-password' to enter the project password
without touching the project slug or env config.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireLogin()
			if err != nil {
				return err
			}

			// --rotate-password: re-enter password on a new machine without
			// re-doing the full init flow.
			if rotatePassword {
				return rotateProjectPassword()
			}

			client := api.New(cfg)
			reader := bufio.NewReader(os.Stdin)

			fmt.Println()
			fmt.Println("🔗 Link to a DotSync project")
			fmt.Println("────────────────────────────")
			fmt.Println()

			projects, listErr := client.ListProjects()
			if listErr == nil && len(projects) > 0 {
				fmt.Println("Your projects:")
				for _, p := range projects {
					fmt.Printf("  • "+bold("%s")+" (slug: "+cyan("%s")+")\n", p["name"], p["slug"])
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
				ProjectSlug: slug,
				DefaultEnv:  env,
			}
			if err := config.SaveProject(projCfg); err != nil {
				return fmt.Errorf("save project config: %w", err)
			}

			if err := config.SetProjectPassword(slug, password); err != nil {
				return err
			}

			ensureGitignore()

			fmt.Println()
			fmt.Printf(green("✅ Linked to project '%s' (env: %s)")+"\n", slug, env)
			fmt.Println()
			fmt.Println("  dotsync push    # upload your .env")
			fmt.Println("  dotsync pull    # download latest .env")
			fmt.Println()
			fmt.Println("  On a new machine: run 'dotsync init --rotate-password'")
			fmt.Println("  to enter the same password there.")
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().BoolVar(&create, "new", false, "create a new project")
	cmd.Flags().BoolVar(&rotatePassword, "rotate-password", false,
		"re-enter the project password on this machine (use after cloning on a new device)")
	return cmd
}

// rotateProjectPassword lets the user re-enter the password for an already-linked
// project. This is the primary workflow for setting up a second machine:
// clone the repo (which has .dotsync.json), then run dotsync init --rotate-password.
func rotateProjectPassword() error {
	projCfg, err := config.LoadProject()
	if err != nil {
		return fmt.Errorf("no project linked in this directory — run 'dotsync init' first")
	}

	fmt.Printf("\n🔑 Set password for project '%s'\n", projCfg.ProjectSlug)
	fmt.Println("────────────────────────────────")
	fmt.Println("Enter the same password used on your other machine.")
	fmt.Println()

	password, err := readPassword("Project Password: ")
	if err != nil {
		return err
	}

	if err := config.SetProjectPassword(projCfg.ProjectSlug, password); err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf(green("✅ Password saved for project '%s'")+"\n", projCfg.ProjectSlug)
	fmt.Println()
	fmt.Println("  You can now run: dotsync pull")
	fmt.Println()
	return nil
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

	// Confirm to catch typos — a wrong password = permanently unrecoverable secrets
	confirm, err := readPassword("Confirm Password: ")
	if err != nil {
		return err
	}
	if confirm != password {
		return fmt.Errorf("passwords do not match — try again")
	}

	fmt.Print(dim("⏳ Creating project..."))
	proj, err := client.CreateProject(name, slug, desc)
	if err != nil {
		fmt.Println(" ❌")
		return err
	}
	fmt.Println(green(" ✅"))

	actualSlug := proj["slug"].(string)

	projCfg := &config.ProjectConfig{
		ProjectSlug: actualSlug,
		DefaultEnv:  "dev",
	}
	if err := config.SaveProject(projCfg); err != nil {
		return fmt.Errorf("save project config: %w", err)
	}

	if err := config.SetProjectPassword(actualSlug, password); err != nil {
		return err
	}

	ensureGitignore()

	fmt.Printf("\n"+green("✅ Project '%s' created and linked!")+"\n", name)
	fmt.Println("\n  3 environments auto-created: dev, staging, production")
	fmt.Println("  Run: dotsync push")
	fmt.Println()

	return nil
}

// readPassword reads a password from stdin with echo disabled.
func readPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		pw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		if strings.TrimSpace(string(pw)) == "" {
			return "", fmt.Errorf("password cannot be empty")
		}
		return string(pw), nil
	}
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

	fmt.Println("  "+dim("📝 Added .env to .gitignore"))
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
