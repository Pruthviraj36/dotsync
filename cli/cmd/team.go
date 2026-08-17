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

// teamIndent is the fixed left margin for all team table rows.
// Must match the visual width of the marker " ▶  " (4 visual chars + 1 space).
const teamIndent = "     " // 5 spaces = "  " header indent + 3 marker chars

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

			if len(members) == 0 {
				blank()
				fmt.Println(info("No members yet."))
				blank()
				cmdHint("dotsync team add <github-username>")
				blank()
				return nil
			}

			// Measure column widths from raw content
			userW, roleW := 8, 4
			for _, m := range members {
				u, _ := m["username"].(string)
				r, _ := m["role"].(string)
				if w := len("@" + u); w > userW { userW = w }
				if w := len(r);       w > roleW { roleW = w }
			}
			rw := userW + roleW + 12 + 6
			ruler := ruleN(rw)

			blank()
			fmt.Println(prog("Team", boldCyan(projCfg.ProjectSlug)))
			blank()
			tableHeader(ruler,
				[]string{"USERNAME", "ROLE", "JOINED"},
				[]int{userW, roleW},
			)

			indent := strings.Repeat(" ", labelW+2)
			for _, m := range members {
				username, _ := m["username"].(string)
				role, _     := m["role"].(string)
				joinedAt, _ := m["joined_at"].(string)
				age := ""
				if len(joinedAt) >= 10 {
					age = joinedAt[:10]
				}
				unameRaw := "@" + username
				isMe := username == cfg.Username

				if isMe {
					// Highlight the current user's row entirely in bold cyan
					fmt.Printf("%s%s  %s  %s\n",
						indent,
						padRight(boldCyan(unameRaw), userW),
						padRight(boldCyan(role), roleW),
						boldCyan(age),
					)
				} else {
					fmt.Printf("%s%s  %s  %s\n",
						indent,
						padRight(colCyan(unameRaw, userW), userW),
						padRight(roleColor(role), roleW),
						dim(age),
					)
				}
			}

			fmt.Printf("%s%s\n", indent, ruler)
			blank()
			hint("add:    dotsync team add <username>")
			hint("remove: dotsync team remove <username>")
			hint("role:   dotsync team role <username> admin|member|viewer")
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
		Args:    cobra.ExactArgs(1),
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

			fmt.Printf("%s  Adding @%s...\n", spin(""), username)

			if err := client.AddTeamMember(projCfg.ProjectSlug, username); err != nil {
				return err
			}

			if roleFlag != "member" {
				if err := client.UpdateTeamRole(projCfg.ProjectSlug, username, roleFlag); err != nil {
					fmt.Println(warn(fmt.Sprintf("Added but could not set role to %s: %v", roleFlag, err)))
					return nil
				}
			}

			blank()
			fmt.Printf("%s  @%s added to %s as %s\n",
				boldGreen("success"), username, boldCyan(projCfg.ProjectSlug), roleColor(roleFlag))
			blank()
			hint(fmt.Sprintf("Tell @%s to run:", username))
			cmdHint("  dotsync init")
			blank()
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
				fmt.Printf("  Remove @%s from %s? [y/N]: ", username, projCfg.ProjectSlug)
				var confirm string
				fmt.Scanln(&confirm)
				if confirm != "y" && confirm != "Y" {
					fmt.Println(dim("  Aborted — nothing changed."))
					return nil
				}
			}

			client := api.New(cfg)
			if err := client.RemoveTeamMember(projCfg.ProjectSlug, username); err != nil {
				return err
			}

			blank()
			fmt.Printf("%s  @%s removed from %s\n",
				boldGreen("success"), username, boldCyan(projCfg.ProjectSlug))
			blank()
			hint("Note: they may still have a local .env from a previous pull.")
			hint("If this is a security concern, push new secrets immediately:")
			cmdHint("  dotsync push")
			blank()
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
		Args:    cobra.ExactArgs(2),
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
			if err := client.UpdateTeamRole(projCfg.ProjectSlug, username, role); err != nil {
				return err
			}

			blank()
			fmt.Printf("%s  @%s is now %s in %s\n",
				boldGreen("success"), username, roleColor(role), boldCyan(projCfg.ProjectSlug))
			blank()
			return nil
		},
	}
}
