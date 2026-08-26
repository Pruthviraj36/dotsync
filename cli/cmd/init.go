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
				fmt.Println(ok(fmt.Sprintf("%s  %s", dim("logged in as"), boldCyan("@"+cfg.Username))))
				blank()
				fmt.Println(msgPad() + bold("your projects"))
				for _, p := range projects {
					fmt.Printf("%s  %s  %s\n",
						msgPad(),
						boldCyan(fmt.Sprintf("%v", p["slug"])),
						dim(fmt.Sprintf("%v", p["name"])),
					)
				}
				blank()
			}

			fmt.Printf("%sProject slug (or %s to create new): ", msgPad(), cyan("new"))
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
							"%sRun %s to see your projects\n"+
							"%sRun %s to create it",
						slug,
						msgPad(), cyan("dotsync init"),
						msgPad(), cyan("dotsync init --new"),
					)
				}
			}

			fmt.Printf("%sEnvironment [dev]: ", msgPad())
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

			ensureGitignore()

			blank()
			fmt.Println(ok(boldCyan(slug) + "  " + dim("linked · "+env)))
			blank()
			hint("password fetched automatically on first pull/push")
			cmdHint("dotsync pull")
			blank()
			return nil
		},
	}

	cmd.Flags().BoolVar(&create, "new", false, "create a new project")
	return cmd
}

func createNewProject(client *api.Client, cfg *config.GlobalConfig, reader *bufio.Reader) error {
	blank()
	fmt.Println(bold(msgPad() + "Create a new project"))
	blank()

	fmt.Printf("%sProject name: ", msgPad())
	name, _ := reader.ReadString('\n')
	name = strings.TrimSpace(name)

	fmt.Printf("%sSlug (lowercase, hyphens): ", msgPad())
	slug, _ := reader.ReadString('\n')
	slug = strings.TrimSpace(slug)

	fmt.Printf("%sDescription (optional): ", msgPad())
	desc, _ := reader.ReadString('\n')
	desc = strings.TrimSpace(desc)

	if name == "" || slug == "" {
		return fmt.Errorf("name and slug are required")
	}

	blank()
	password, err := readPassword(msgPad() + "Encryption password: ")
	if err != nil {
		return err
	}
	confirm, err := readPassword(msgPad() + "Confirm password: ")
	if err != nil {
		return err
	}
	if confirm != password {
		return fmt.Errorf("passwords do not match")
	}

	blank()
	fmt.Println(prog("Creating", boldCyan(slug)))
	proj, err := client.CreateProject(name, slug, desc)
	if err != nil {
		return err
	}

	actualSlug := proj["slug"].(string)

	projCfg := &config.ProjectConfig{
		ProjectSlug: actualSlug,
		DefaultEnv:  "dev",
	}
	if err := config.SaveProject(projCfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Println(prog("Securing", dim("encryption key...")))
	if err := setPassword(client, actualSlug, password); err != nil {
		return err
	}

	// Derive key once so any error surfaces here, not at push time
	_ = crypto.DeriveKey(password, actualSlug)

	fmt.Println(prog("Identity", dim("ed25519 signing key...")))
	_, pub, _, err := identity.Ensure()
	if err != nil {
		return fmt.Errorf("ed25519 identity: %w", err)
	}
	if err := client.SetPubKey(identity.Hex(pub)); err != nil {
		fmt.Println(dim(msgPad() + "(public key sync will retry on next push)"))
	}

	ensureGitignore()

	blank()
	fmt.Println(ok(boldCyan(actualSlug) + "  " + dim("ready")))
	blank()
	kv("Project", name)
	kv("Slug", actualSlug)
	kv("Envs", "dev · staging · production")
	kv("Encryption", "AES-256-GCM + Argon2id")
	kv("Signing", "ed25519")
	blank()
	hint("push your first secrets:")
	cmdHint("dotsync push")
	blank()

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
		return nil, fmt.Errorf(msg)
	}
	if !config.IsLoggedIn(cfg) {
		return nil, fmt.Errorf("not logged in\n\n%sdotsync login", msgPad())
	}
	return cfg, nil
}
