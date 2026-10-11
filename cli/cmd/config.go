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

			banner("CLI Configuration")

			serverVal := cfg.ServerURL
			serverStatus := green("configured")
			if serverVal == "" {
				serverVal = "not configured"
				serverStatus = yellow("missing")
			}

			accountVal := "@" + cfg.Username
			accountStatus := green("authenticated")
			if !config.IsLoggedIn(cfg) {
				accountVal = "not logged in"
				accountStatus = dim("unauthenticated")
			}

			var rows [][]string
			rows = append(rows, []string{"Server Endpoint", serverVal, serverStatus})
			rows = append(rows, []string{"Active Account", accountVal, accountStatus})

			projCfg, _ := config.LoadProject()
			if projCfg != nil {
				rows = append(rows, []string{"Linked Project", projCfg.ProjectSlug, green("linked")})
				rows = append(rows, []string{"Default Env", projCfg.DefaultEnv, dim("active")})
			}

			table([]string{"Configuration Key", "Value", "Status"}, rows)
			hint("Change server:   dotsync config set-server <url>")
			hint("Environment var: export DOTSYNC_SERVER=<url>")
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

			banner("Configure Server Endpoint")
			txRunning("configuration update")
			txStep(1, 1, fmt.Sprintf("Writing endpoint '%s' to configuration", args[0]))

			transactionSummary("Configuration Summary", [][2]string{
				{"Server URL", args[0]},
				{"Storage Path", "~/.dotsync/config.json"},
			})

			hint("Next step: authenticate with 'dotsync login'")
			complete("Server configuration updated.")
			return nil
		},
	}
}
