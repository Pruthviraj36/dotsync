package cmd

import (
	"fmt"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
	"github.com/spf13/cobra"
)

func teamCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "team",
		Short: "Manage project team members",
	}

	cmd.AddCommand(teamAddCmd())
	return cmd
}

func teamAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <username>",
		Short: "Invite a GitHub user to your project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireLogin()
			if err != nil {
				return err
			}

			projCfg, err := config.LoadProject()
			if err != nil {
				return err
			}

			username := args[0]
			client := api.New(cfg)

			fmt.Printf("⏳ Inviting %s to project '%s'...\n", username, projCfg.ProjectSlug)
			if err := client.AddTeamMember(projCfg.ProjectSlug, username); err != nil {
				return err
			}

			fmt.Printf("✅ %s has been added to '%s'!\n", username, projCfg.ProjectSlug)
			return nil
		},
	}
}
