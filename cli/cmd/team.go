package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
)

func teamCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "team",
		Short: "Manage project team members",
	}
	cmd.AddCommand(
		teamListCmd(),
		teamAddCmd(),
		teamRemoveCmd(),
		teamRoleCmd(),
	)
	return cmd
}

func teamListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all team members and their roles",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireLogin()
			if err != nil {
				return err
			}
			projCfg, err := config.LoadProject()
			if err != nil {
				return err
			}

			client := api.New(cfg)
			members, err := client.ListTeamMembers(projCfg.ProjectSlug)
			if err != nil {
				return err
			}

			if len(members) == 0 {
				fmt.Println("No members found.")
				return nil
			}

			fmt.Printf("\n"+bold("👥 Team for project '%s'")+"\n", projCfg.ProjectSlug)
			fmt.Println(strings.Repeat("─", 50))
			fmt.Printf("  "+bold("%-25s")+" "+bold("%-10s")+" "+bold("%s")+"\n", "USERNAME", "ROLE", "JOINED")
			fmt.Println(strings.Repeat("─", 50))

			for _, m := range members {
				username, _ := m["username"].(string)
				role, _ := m["role"].(string)
				joinedAt, _ := m["joined_at"].(string)

				roleLabel := roleWithIcon(role)
				age := ""
				if joinedAt != "" {
					age = joinedAt[:10] // YYYY-MM-DD
				}

				// Highlight the current user
				marker := "  "
				if username == cfg.Username {
					marker = green("→ ")
				}

				fmt.Printf("%s"+cyan("%-25s")+" %-15s "+dim("%s")+"\n", marker, "@"+username, roleLabel, age)
			}

			fmt.Println(strings.Repeat("─", 50))
			fmt.Printf("  %d member(s)\n\n", len(members))
			fmt.Println("Roles: owner > admin > member > viewer")
			fmt.Println("  dotsync team add <username>")
			fmt.Println("  dotsync team remove <username>")
			fmt.Println("  dotsync team role <username> <role>")
			fmt.Println()
			return nil
		},
	}
}

func teamAddCmd() *cobra.Command {
	var roleFlag string

	cmd := &cobra.Command{
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

			fmt.Printf(dim("⏳ Inviting @%s to '%s' as %s...")+"\n",
				username, projCfg.ProjectSlug, roleFlag)

			if err := client.AddTeamMember(projCfg.ProjectSlug, username); err != nil {
				return err
			}

			// Set role if not the default "member"
			if roleFlag != "member" {
				if err := client.UpdateTeamRole(projCfg.ProjectSlug, username, roleFlag); err != nil {
					fmt.Printf(yellow("⚠️  Added, but could not set role to %s: %v")+"\n", roleFlag, err)
					return nil
				}
			}

			fmt.Printf(green("✅ @%s added to '%s' as %s")+"\n",
				username, projCfg.ProjectSlug, roleWithIcon(roleFlag))
			fmt.Println()
			fmt.Printf("  They'll need to run: dotsync init\n")
			fmt.Printf("  Then share your project password with them securely.\n")
			fmt.Println()
			return nil
		},
	}

	cmd.Flags().StringVar(&roleFlag, "role", "member", "role: admin, member, viewer")
	return cmd
}

func teamRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <username>",
		Short:   "Remove a member from the project",
		Aliases: []string{"rm"},
		Args:    cobra.ExactArgs(1),
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

			fmt.Printf("Remove @%s from '%s'? [y/N]: ", username, projCfg.ProjectSlug)
			var confirm string
			fmt.Scanln(&confirm)
			if confirm != "y" && confirm != "Y" {
				fmt.Println("Aborted.")
				return nil
			}

			client := api.New(cfg)
			if err := client.RemoveTeamMember(projCfg.ProjectSlug, username); err != nil {
				return err
			}

			fmt.Printf(green("✅ @%s removed from '%s'")+"\n", username, projCfg.ProjectSlug)
			fmt.Println()
			fmt.Println("  Note: they still have any locally pulled .env files.")
			fmt.Println("  Rotate your project password if this was a security removal:")
			fmt.Println("  dotsync init --rotate-password && dotsync push")
			fmt.Println()
			return nil
		},
	}
}

func teamRoleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "role <username> <role>",
		Short: "Change a team member's role",
		Long: `Changes a team member's role. Valid roles:
  owner   — full control (only one per project, cannot be changed here)
  admin   — can push/pull all envs, invite/remove members
  member  — can push/pull (default)
  viewer  — pull only, cannot push`,
		Args: cobra.ExactArgs(2),
		Example: `  dotsync team role alice admin
  dotsync team role bob viewer`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireLogin()
			if err != nil {
				return err
			}
			projCfg, err := config.LoadProject()
			if err != nil {
				return err
			}

			username, role := args[0], args[1]
			validRoles := map[string]bool{"admin": true, "member": true, "viewer": true}
			if !validRoles[role] {
				return fmt.Errorf("invalid role '%s' — must be: admin, member, viewer", role)
			}

			client := api.New(cfg)
			if err := client.UpdateTeamRole(projCfg.ProjectSlug, username, role); err != nil {
				return err
			}

			fmt.Printf(green("✅ @%s is now %s in '%s'")+"\n",
				username, roleWithIcon(role), projCfg.ProjectSlug)
			return nil
		},
	}
}

func roleWithIcon(role string) string {
	icons := map[string]string{
		"owner":  "👑 owner",
		"admin":  "🔧 admin",
		"member": "👤 member",
		"viewer": "👁  viewer",
	}
	if label, ok := icons[role]; ok {
		return label
	}
	return role
}
