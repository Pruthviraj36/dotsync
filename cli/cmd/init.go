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
	"github.com/Pruthviraj36/dotsync/cli/identity"
	"github.com/Pruthviraj36/dotsync/internal/crypto"
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
				return rotateProjectPassword(cfg)
			}

			client := api.New(cfg)
			reader := bufio.NewReader(os.Stdin)

			fmt.Println()
			fmt.Println(bold("Link to a DotSync project"))
			fmt.Println("────────────────────────────")
			fmt.Println()

			projects, listErr := client.ListProjects()
			if listErr == nil && len(projects) > 0 {
				fmt.Println("Your projects:")
				for _, p := range projects {
					fmt.Printf("  - "+bold("%s")+" (slug: "+cyan("%s")+")\n", p["name"], p["slug"])
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

			// Joining an existing project — the password is fetched automatically
			// from the server by resolvePassword() at push/pull time.
			// Do NOT call setPassword here: only owners and admins can set it.

			ensureGitignore()

			fmt.Println()
			fmt.Println(ok(fmt.Sprintf("Linked to project '%s' (env: %s)", slug, env)))
			fmt.Println()
			fmt.Println("  dotsync pull    # download latest .env")
			fmt.Println("  dotsync push    # upload your .env (if you have write access)")
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
func rotateProjectPassword(cfg *config.GlobalConfig) error {
	projCfg, err := config.LoadProject()
	if err != nil {
		return fmt.Errorf("no project linked in this directory — run 'dotsync init' first")
	}

	fmt.Printf("\n%s\n", bold(fmt.Sprintf("Set password for project '%s'", projCfg.ProjectSlug)))
	fmt.Println("────────────────────────────────")
	fmt.Println("Enter the same password used on your other machine.")
	fmt.Println()

	password, err := readPassword("Project Password: ")
	if err != nil {
		return err
	}

	client := api.New(cfg)
	if err := setPassword(client, projCfg.ProjectSlug, password); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println(ok(fmt.Sprintf("Password saved for project '%s'", projCfg.ProjectSlug)))
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

	fmt.Println(spin("Creating project..."))
	proj, err := client.CreateProject(name, slug, desc)
	if err != nil {
		return err
	}
	fmt.Println(ok("Project created on server"))

	actualSlug := proj["slug"].(string)

	projCfg := &config.ProjectConfig{
		ProjectSlug: actualSlug,
		DefaultEnv:  "dev",
	}
	if err := config.SaveProject(projCfg); err != nil {
		return fmt.Errorf("save project config: %w", err)
	}

	if err := setPassword(client, actualSlug, password); err != nil {
		return err
	}

	// Derive once now (and discard the result) purely so the parameters
	// below are describing something that actually just happened, not a
	// promise about what will happen later at push/pull time.
	_ = crypto.DeriveKey(password, actualSlug)

	// Local ed25519 identity — every future push from this machine gets
	// signed with it, so teammates can verify who pushed what.
	_, pub, identityCreated, err := identity.Ensure()
	if err != nil {
		return fmt.Errorf("ed25519 identity: %w", err)
	}
	if err := client.SetPubKey(identity.Hex(pub)); err != nil {
		fmt.Println(dim("  (could not sync public key yet — will retry on next push)"))
	}

	ensureGitignore()

	fmt.Println()
	fmt.Println(ok("argon2id key derived (mem=64MiB, t=3, p=4)"))
	if identityCreated {
		fmt.Println(ok(fmt.Sprintf("ed25519 identity created: %s", identity.PubKeyPath())))
	}
	// Not "zero-knowledge" — the shared project password is held server-side,
	// encrypted at rest, precisely so a new teammate can fetch it instead of
	// you re-typing it on every machine (see: dotsync init --rotate-password).
	// The .env contents themselves never leave this machine unencrypted.
	fmt.Println(ok("project registered; secrets encrypted client-side"))

	fmt.Println()
	fmt.Println(ok(fmt.Sprintf("Project '%s' created and linked", name)))
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

	fmt.Println("  " + info("Added .env entries to .gitignore"))
}

// requireLogin loads config, checks server URL, and validates login state.
func requireLogin() (*config.GlobalConfig, error) {
	cfg, err := config.LoadGlobal()
	if err != nil {
		return nil, err
	}
	if !config.IsServerConfigured(cfg) {
		return nil, fmt.Errorf(
			"no server configured\n\n" +
				"  Set the DOTSYNC_SERVER environment variable:\n" +
				"    export DOTSYNC_SERVER=https://your-server.example.com\n\n" +
				"  Or save it permanently:\n" +
				"    dotsync config set-server https://your-server.example.com",
		)
	}
	if !config.IsLoggedIn(cfg) {
		return nil, fmt.Errorf("not logged in — run: dotsync login")
	}
	return cfg, nil
}
