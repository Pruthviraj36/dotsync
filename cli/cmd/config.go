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
			fmt.Println("Server URL:", cfg.ServerURL)
			if config.IsLoggedIn(cfg) {
				fmt.Println("Logged in as:", cfg.Username)
			} else {
				fmt.Println("Not logged in")
			}
			return nil
		},
	}
}

func configSetServerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-server <url>",
		Short: "Point the CLI at a different DotSync server",
		Long: `Changes which server this CLI talks to, persisted in ~/.dotsync/config.json.

Useful if the server URL ever changes after you've already logged in —
otherwise the CLI keeps using whatever URL was active at your last login,
indefinitely, with no automatic way to discover that it moved.

This does not log you out of the new server; run 'dotsync login' again
after switching.`,
		Args: cobra.ExactArgs(1),
		Example: `  dotsync config set-server https://dotsync.onrender.com
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
			fmt.Println("✅ Server URL set to", args[0])
			fmt.Println("   Run 'dotsync login' to authenticate with this server.")
			return nil
		},
	}
}
