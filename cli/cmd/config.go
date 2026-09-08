package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/config"
)

func configCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "config",
		Short: "View or change CLI configuration",
	}
	c.AddCommand(configShowCmd(), configSetServerCmd())
	return c
}

func configShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print current CLI configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadGlobal()
			if err != nil {
				return err
			}

			sectionTitle("CLI configuration")

			if cfg.ServerURL != "" {
				kvCyan("server", cfg.ServerURL)
			} else {
				kv("server", red("not set  ")+dim("→ dotsync config set-server <url>"))
			}

			if config.IsLoggedIn(cfg) {
				kvCyan("account", "@"+cfg.Username)
			} else {
				kv("account", dim("not logged in  ")+dim("→ dotsync login"))
			}

			blank()
			hint("Change server: dotsync config set-server <url>")
			hint("Override with: export DOTSYNC_SERVER=<url>")
			blank()
			return nil
		},
	}
}

func configSetServerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-server <url>",
		Short: "Point the CLI at a different DotSync server",
		Long: `Saves the server URL to ~/.dotsync/config.json.

The DOTSYNC_SERVER environment variable always takes precedence over
this saved value — useful for CI/CD or temporary server switches.`,
		Args: cobra.ExactArgs(1),
		Example: `  dotsync config set-server https://your-server.example.com
  dotsync config set-server http://localhost:8080`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadGlobal()
			if err != nil {
				return err
			}
			cfg.ServerURL = args[0]
			if err := config.SaveGlobal(cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}

			blank()
			fmt.Println(ok("Server URL saved"))
			blank()
			kvCyan("server", args[0])
			blank()
			hint("Now authenticate:")
			cmdHint("dotsync login")
			blank()
			return nil
		},
	}
}
