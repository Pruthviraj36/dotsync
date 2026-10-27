package cmd

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
	"github.com/Pruthviraj36/dotsync/cli/identity"
)

var projectSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,37}[a-z0-9]$`)

const minProjectPasswordLen = 12

func initCmd() *cobra.Command {
	var create bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Link this folder to a DotSync project",
		Long: `Creates or links this directory to a DotSync project.

A .dotsync.json file is created here — commit it, not your .env.
On a new machine: run dotsync init with the same slug.
The encryption password is fetched automatically.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireLogin()
			if err != nil {
				return err
			}

			client := api.New(cfg)
			reader := bufio.NewReader(os.Stdin)

			projects, listErr := client.ListProjects()

			if create {
				return createNewProject(client, cfg, reader)
			}

			blank()
			if listErr == nil && len(projects) > 0 {
				banner("DotSync Project Initialization")
				fmt.Printf(" Authenticated as @%s on %s\n\n", cfg.Username, cfg.ServerURL)
				rows := make([][]string, len(projects))
				for i, p := range projects {
					rows[i] = []string{fmt.Sprintf("%v", p["slug"]), fmt.Sprintf("%v", p["name"])}
				}
				table([]string{"Project Slug", "Project Name"}, rows)
				fmt.Println()
			} else {
				banner("DotSync Project Initialization")
			}

			fmt.Print(" Project slug (or 'new' to create): ")
			slug, _ := reader.ReadString('\n')
			slug = strings.TrimSpace(slug)

			if slug == "new" || slug == "" && create {
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
						"project %q not found\n\n"+
							"  Run 'dotsync init' to view projects\n"+
							"  Run 'dotsync init --new' to create a new project",
						slug,
					)
				}
			}

			fmt.Print(" Environment [dev]: ")
			env, _ := reader.ReadString('\n')
			env = strings.TrimSpace(env)
			if env == "" {
				env = "dev"
			}
			if env != "dev" && env != "staging" && env != "production" {
				return fmt.Errorf("environment must be: dev, staging, or production")
			}

			projCfg := &config.ProjectConfig{
				ProjectSlug: slug,
				DefaultEnv:  env,
			}
			if err := config.SaveProject(projCfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}

			txRunning("linking")
			txStep(1, 2, fmt.Sprintf("Saved project binding in .dotsync.json (%s/%s)", slug, env))
			ensureGitignore()
			txStep(2, 2, "Verified .gitignore protection")

			transactionSummary("Project Configuration", [][2]string{
				{"Project Slug", slug},
				{"Default Env", env},
				{"Config File", ".dotsync.json"},
			})

			complete(fmt.Sprintf("Linked workspace to %s/%s", slug, env))
			return nil
		},
	}

	cmd.Flags().BoolVar(&create, "new", false, "create a new project")
	return cmd
}

func createNewProject(client *api.Client, cfg *config.GlobalConfig, reader *bufio.Reader) error {
	banner("Create New Project")

	fmt.Print(" Project name: ")
	name, _ := reader.ReadString('\n')
	name = strings.TrimSpace(name)

	fmt.Print(" Slug (lowercase, hyphens): ")
	slug, _ := reader.ReadString('\n')
	slug = strings.TrimSpace(slug)

	fmt.Print(" Description (optional): ")
	desc, _ := reader.ReadString('\n')
	desc = strings.TrimSpace(desc)

	if name == "" || slug == "" {
		return fmt.Errorf("name and slug are required")
	}
	slug = strings.ToLower(slug)
	if !projectSlugPattern.MatchString(slug) || strings.Contains(slug, "--") {
		return fmt.Errorf("slug must be 2-39 characters: lowercase letters, digits, and single hyphens, starting with a letter or digit")
	}

	password, err := readPassword(" Encryption password: ")
	if err != nil {
		return err
	}
	confirm, err := readPassword(" Confirm password: ")
	if err != nil {
		return err
	}
	if confirm != password {
		return fmt.Errorf("passwords do not match")
	}
	if len(password) < minProjectPasswordLen {
		return fmt.Errorf("password must be at least %d characters — it protects every secret in this project", minProjectPasswordLen)
	}

	txRunning("creation")

	proj, err := client.CreateProject(name, slug, desc)
	if err != nil {
		return err
	}

	actualSlug, _ := proj["slug"].(string)
	if actualSlug == "" {
		return fmt.Errorf("server response did not include a project slug — please try again")
	}

	projCfg := &config.ProjectConfig{
		ProjectSlug: actualSlug,
		DefaultEnv:  "dev",
	}
	if err := config.SaveProject(projCfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	txStep(1, 3, fmt.Sprintf("Created project record '%s' on server", actualSlug))

	if err := setPassword(client, actualSlug, password); err != nil {
		return err
	}
	txStep(2, 3, "Encrypted and secured project password on server")

	_, pub, _, err := identity.Ensure()
	if err != nil {
		return fmt.Errorf("ed25519 identity: %w", err)
	}
	if err := client.SetPubKey(identity.Hex(pub)); err != nil {
		fmt.Println("  " + warn("public key sync will retry on next push"))
	}
	txStep(3, 3, "Initialized ed25519 signing identity")

	ensureGitignore()

	transactionSummary("New Project Summary", [][2]string{
		{"Project Name", name},
		{"Project Slug", actualSlug},
		{"Environments", "dev, staging, production"},
		{"Encryption", "AES-256-GCM + Argon2id"},
		{"Signing", "ed25519"},
	})

	complete(fmt.Sprintf("Created project '%s'", actualSlug))
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

// ensureGitignore adds DotSync's secret files to .gitignore if not already
// present. Matching is line-based (after trimming) instead of a substring
// search: `strings.Contains(content, ".env")` silently treated an existing
// entry like ".env.example" or "foo.env" as if ".env" itself were ignored.
func ensureGitignore() {
	data, _ := os.ReadFile(".gitignore")
	content := string(data)

	ignored := make(map[string]bool)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ignored[line] = true
	}

	var toAdd []string
	for _, entry := range []string{".env", ".env.local"} {
		if !ignored[entry] && !ignored["/"+entry] && !ignored["**/"+entry] {
			toAdd = append(toAdd, entry)
		}
	}
	if len(toAdd) == 0 {
		return
	}

	f, err := os.OpenFile(".gitignore", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Println(warn("could not update .gitignore — make sure " + strings.Join(toAdd, ", ") + " is ignored"))
		return
	}
	defer f.Close()

	if len(data) > 0 && !strings.HasSuffix(content, "\n") {
		f.WriteString("\n")
	}
	f.WriteString("\n# dotsync — never commit secrets\n")
	for _, entry := range toAdd {
		f.WriteString(entry + "\n")
	}

	fmt.Println(info("added " + strings.Join(toAdd, ", ") + " to .gitignore"))
}

// requireLogin loads config, checks server URL, and validates login state.
func requireLogin() (*config.GlobalConfig, error) {
	cfg, err := config.LoadGlobal()
	if err != nil {
		return nil, err
	}
	if !config.IsServerConfigured(cfg) {
		msg := "no server configured\n\n" +
			msgPad() + "save it permanently:\n" +
			msgPad() + "  dotsync config set-server https://your-server.example.com\n\n" +
			msgPad() + "or set per-session:\n" +
			msgPad() + "  export DOTSYNC_SERVER=https://your-server.example.com\n"
		if os.Getenv("SUDO_USER") != "" || os.Getenv("SUDO_UID") != "" {
			msg += "\n" + msgPad() + "running under sudo? preserve env with:\n" +
				msgPad() + "  sudo -E dotsync run -- your-command\n"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	if !config.IsLoggedIn(cfg) {
		return nil, fmt.Errorf("not logged in\n\n%sdotsync login", msgPad())
	}
	return cfg, nil
}
