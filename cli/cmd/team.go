package cmd

import (
	"fmt"

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
				blank()
				fmt.Println(info("No members found."))
				blank()
				return nil
			}

			// Dynamic column widths
			userW, roleW, joinW := 8, 4, 6
			for _, m := range members {
				u, _ := m["username"].(string)
				r, _ := m["role"].(string)
				if w := len("@" + u); w > userW { userW = w }
				if w := len(r);       w > roleW { roleW = w }
			}
			rw := userW + roleW + joinW + 12
			rl := ruleN(rw)

			blank()
			fmt.Printf("  %s  %s\n", bold("Team"), boldCyan(projCfg.ProjectSlug))
			blank()
			tableHeader(rl,
				[]string{"USERNAME", "ROLE", "JOINED"},
				[]int{userW, roleW},
			)

			for _, m := range members {
				username, _ := m["username"].(string)
				role, _     := m["role"].(string)
				joinedAt, _ := m["joined_at"].(string)

				age := ""
				if len(joinedAt) >= 10 {
					age = joinedAt[:10]
				}

				marker := "   "
				uname := cyan("@" + username)
				if username == cfg.Username {
					marker = green(" ▶ ")
					uname = boldCyan("@" + username)
				}

				fmt.Printf("%s%-*s  %-*s  %s\n",
					marker, userW, uname, roleW, roleColor(role), dim(age))
			}

			fmt.Println("  " + rl)
			fmt.Printf("  %s\n", dim(fmt.Sprintf(
				"%d member(s)  ·  roles: owner > admin > member > viewer", len(members))))
			blank()
			hint("dotsync team add <username>")
			hint("dotsync team remove <username>")
			hint("dotsync team role <username> <role>")
			blank()
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

			fmt.Printf("%s  Adding @%s to %s as %s...\n",
				spin(""), username, projCfg.ProjectSlug, roleFlag)

			if err := client.AddTeamMember(projCfg.ProjectSlug, username); err != nil {
				return err
			}

			if roleFlag != "member" {
				if err := client.UpdateTeamRole(projCfg.ProjectSlug, username, roleFlag); err != nil {
					fmt.Println(warn(fmt.Sprintf("Added, but could not set role to %s: %v", roleFlag, err)))
					return nil
				}
			}

			blank()
			fmt.Println(ok(fmt.Sprintf("@%s added to %s as %s",
				username, projCfg.ProjectSlug, roleColor(roleFlag))))
			blank()
			hint("They'll need to run: dotsync init")
			blank()
			return nil
		},
	}

	cmd.Flags().StringVar(&roleFlag, "role", "member", "role: admin | member | viewer")
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

			fmt.Printf("%s  Remove @%s from %s? [y/N]: ",
				warn(""), username, projCfg.ProjectSlug)
			var confirm string
			fmt.Scanln(&confirm)
			if confirm != "y" && confirm != "Y" {
				fmt.Println(dim("  Aborted."))
				return nil
			}

			client := api.New(cfg)
			if err := client.RemoveTeamMember(projCfg.ProjectSlug, username); err != nil {
				return err
			}

			blank()
			fmt.Println(ok(fmt.Sprintf("@%s removed from %s", username, projCfg.ProjectSlug)))
			blank()
			hint("They may still have locally pulled .env files.")
			hint("If this was a security removal, rotate your password:")
			cmdHint("dotsync init --rotate-password && dotsync push")
			blank()
			return nil
		},
	}
}

func teamRoleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "role <username> <role>",
		Short: "Change a team member's role",
		Long: `Changes a team member's role.

  owner   full control (set at project creation, cannot be changed here)
  admin   push/pull all envs, invite/remove members
  member  push/pull (default)
  viewer  pull only`,
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
				return fmt.Errorf("invalid role %q — must be: admin, member, viewer", role)
			}

			client := api.New(cfg)
			if err := client.UpdateTeamRole(projCfg.ProjectSlug, username, role); err != nil {
				return err
			}

			blank()
			fmt.Println(ok(fmt.Sprintf("@%s is now %s in %s",
				username, roleColor(role), projCfg.ProjectSlug)))
			blank()
			return nil
		},
	}
}
