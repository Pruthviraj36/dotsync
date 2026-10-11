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
		Long: `Manage who has access to your project.

Roles:
  owner   Full control. Set at project creation.
  admin   Push/pull all envs, invite and remove members.
  member  Push and pull secrets. (default)
  viewer  Pull only — read-only access.`,
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
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all team members and their roles",
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

			banner("Team Members · " + projCfg.ProjectSlug)

			if len(members) == 0 {
				fmt.Println(info("No team members found."))
				blank()
				hint("Add a member: dotsync team add <github-username>")
				blank()
				return nil
			}

			var rows [][]string
			for _, m := range members {
				username, _ := m["username"].(string)
				role, _ := m["role"].(string)
				joinedAt, _ := m["joined_at"].(string)
				age := ""
				if len(joinedAt) >= 10 {
					age = joinedAt[:10]
				}
				unameRaw := "@" + username
				if username == cfg.Username {
					unameRaw += " (you)"
				}
				rows = append(rows, []string{unameRaw, roleColor(role), age, "active"})
			}
			table([]string{"Member", "Role", "Joined", "Status"}, rows)

			hint("Add member:    dotsync team add <username>")
			hint("Remove member: dotsync team remove <username>")
			hint("Change role:   dotsync team role <username> <admin|member|viewer>")
			blank()
			return nil
		},
	}
}

func teamAddCmd() *cobra.Command {
	var roleFlag string

	cmd := &cobra.Command{
		Use:   "add <github-username>",
		Short: "Invite a GitHub user to your project",
		Long: `Grants a GitHub user access to your project.

They don't need to do anything to accept — just run dotsync init
with your project slug. The password is fetched automatically.`,
		Args: cobra.ExactArgs(1),
		Example: `  dotsync team add alice
  dotsync team add bob --role viewer`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireLogin()
			if err != nil {
				return err
			}
			projCfg, err := config.LoadProject()
			if err != nil {
				return err
			}

			username := strings.TrimPrefix(args[0], "@")
			client := api.New(cfg)

			banner("Team · Add Member")
			txRunning("membership transaction")

			txStep(1, 2, fmt.Sprintf("Granting @%s access to %s", username, projCfg.ProjectSlug))
			if err := client.AddTeamMember(projCfg.ProjectSlug, username); err != nil {
				return err
			}

			if roleFlag != "member" {
				txStep(2, 2, fmt.Sprintf("Assigning role '%s'", roleFlag))
				if err := client.UpdateTeamRole(projCfg.ProjectSlug, username, roleFlag); err != nil {
					fmt.Println(warn(fmt.Sprintf("Added but could not set role to %s: %v", roleFlag, err)))
				}
			} else {
				txStep(2, 2, "Default role 'member' assigned")
			}

			transactionSummary("Membership Update", [][2]string{
				{"Project", projCfg.ProjectSlug},
				{"Member", "@" + username},
				{"Role", roleFlag},
			})

			hint(fmt.Sprintf("Tell @%s to run: dotsync init %s", username, projCfg.ProjectSlug))
			complete(fmt.Sprintf("@%s added successfully.", username))
			return nil
		},
	}

	cmd.Flags().StringVar(&roleFlag, "role", "member", "role: admin | member | viewer")
	return cmd
}

func teamRemoveCmd() *cobra.Command {
	var forceFlag bool

	cmd := &cobra.Command{
		Use:     "remove <github-username>",
		Aliases: []string{"rm"},
		Short:   "Remove a member from the project",
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

			username := strings.TrimPrefix(args[0], "@")

			if !forceFlag {
				fmt.Printf("Remove @%s from %s? [y/N]: ", username, projCfg.ProjectSlug)
				var confirm string
				fmt.Scanln(&confirm)
				if confirm != "y" && confirm != "Y" {
					fmt.Println(dim("Aborted — nothing changed."))
					return nil
				}
			}

			client := api.New(cfg)
			banner("Team · Remove Member")
			txRunning("membership revocation")
			txStep(1, 1, fmt.Sprintf("Revoking access for @%s from %s", username, projCfg.ProjectSlug))

			if err := client.RemoveTeamMember(projCfg.ProjectSlug, username); err != nil {
				return err
			}

			transactionSummary("Membership Revocation", [][2]string{
				{"Project", projCfg.ProjectSlug},
				{"Member", "@" + username},
				{"Status", "Revoked"},
			})

			hint("Member may still possess locally cached secrets from prior pulls.")
			hint("To ensure security, consider rotating secrets: dotsync push")
			complete(fmt.Sprintf("@%s removed from project.", username))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "skip confirmation")
	return cmd
}

func teamRoleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "role <github-username> <role>",
		Short: "Change a team member's role",
		Long: `Changes a team member's role.

  admin   Push/pull all envs, invite and remove members
  member  Push and pull (default)
  viewer  Pull only — read-only`,
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

			username := strings.TrimPrefix(args[0], "@")
			role := args[1]

			validRoles := map[string]bool{"admin": true, "member": true, "viewer": true}
			if !validRoles[role] {
				return fmt.Errorf("invalid role %q — must be: admin, member, viewer", role)
			}

			client := api.New(cfg)
			banner("Team · Update Role")
			txRunning("role update")
			txStep(1, 1, fmt.Sprintf("Updating @%s role to %s", username, role))

			if err := client.UpdateTeamRole(projCfg.ProjectSlug, username, role); err != nil {
				return err
			}

			transactionSummary("Role Update", [][2]string{
				{"Project", projCfg.ProjectSlug},
				{"Member", "@" + username},
				{"New Role", role},
			})

			complete(fmt.Sprintf("@%s role updated to %s.", username, role))
			return nil
		},
	}
}
