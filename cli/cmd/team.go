package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
	cliCrypto "github.com/Pruthviraj36/dotsync/cli/crypto"
	"github.com/Pruthviraj36/dotsync/cli/identity"
	dotsyncCrypto "github.com/Pruthviraj36/dotsync/internal/crypto"
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
		teamRefreshKeyCmd(),
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

They must run dotsync init with your project slug so their device key
is registered. Then the owner publishes their project-key envelope.`,
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
			if err := requireReauth(client, "sensitive"); err != nil {
				return err
			}

			banner("Team · Add Member")
			txRunning("membership transaction")

			txStep(1, 2, fmt.Sprintf("Granting @%s access to %s", username, projCfg.ProjectSlug))
			if err := client.AddTeamMember(projCfg.ProjectSlug, username, roleFlag); err != nil {
				return err
			}
			if err := publishMemberEnvelope(client, projCfg.ProjectSlug, username); err != nil {
				fmt.Println("  " + warn(fmt.Sprintf("member added, but key envelope is pending: %v", err)))
			}
			txStep(2, 2, fmt.Sprintf("Assigned role '%s'", roleFlag))

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

func teamRefreshKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "refresh-key <github-username>",
		Aliases: []string{"publish-key"},
		Short:   "Publish a pending project-key envelope for a member",
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
			client := api.New(cfg)
			if err := requireReauth(client, "sensitive"); err != nil {
				return err
			}
			if err := publishMemberEnvelope(client, projCfg.ProjectSlug, username); err != nil {
				return err
			}
			complete(fmt.Sprintf("Project-key envelope published for @%s.", username))
			return nil
		},
	}
}

func publishMemberEnvelope(client *api.Client, slug, username string) error {
	projectKey, err := identity.LoadProjectKey(slug)
	if err != nil {
		return fmt.Errorf("load local project key: %w", err)
	}
	members, err := client.ListTeamMembers(slug)
	if err != nil {
		return fmt.Errorf("list project members: %w", err)
	}
	var publicKeyHex string
	for _, member := range members {
		if memberUsername, _ := member["username"].(string); strings.EqualFold(memberUsername, username) {
			publicKeyHex, _ = member["public_key"].(string)
			break
		}
	}
	if publicKeyHex == "" {
		return fmt.Errorf("member has not published an encryption key; ask them to log in and run dotsync init")
	}
	publicKey, err := hex.DecodeString(publicKeyHex)
	if err != nil || len(publicKey) != dotsyncCrypto.X25519KeySize {
		return fmt.Errorf("member encryption key is invalid")
	}
	envelope, err := dotsyncCrypto.WrapProjectKey(slug, projectKey, publicKey)
	if err != nil {
		return err
	}
	return client.PutProjectKeyEnvelope(slug, username, api.KeyEnvelope{
		RecipientPubKey: envelope.RecipientPubKey,
		EphemeralPubKey: envelope.EphemeralPubKey,
		Nonce:           envelope.Nonce,
		Ciphertext:      envelope.Ciphertext,
	})
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
			if err := requireReauth(client, "sensitive"); err != nil {
				return err
			}
			banner("Team · Remove Member")
			txRunning("membership revocation")
			if err := rotateProjectKeyAndRemove(client, projCfg.ProjectSlug, username); err != nil {
				return err
			}

			transactionSummary("Membership Revocation", [][2]string{
				{"Project", projCfg.ProjectSlug},
				{"Member", "@" + username},
				{"Status", "Revoked and key rotated"},
			})

			hint("Previously downloaded or historical ciphertext cannot be revoked remotely.")
			complete(fmt.Sprintf("@%s removed from project.", username))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "skip confirmation")
	return cmd
}

func rotateProjectKeyAndRemove(client *api.Client, slug, removedUsername string) error {
	oldKey, err := identity.LoadProjectKey(slug)
	if err != nil {
		return fmt.Errorf("load local project key: %w", err)
	}
	newKey := make([]byte, dotsyncCrypto.ProjectKeySize)
	if _, err := rand.Read(newKey); err != nil {
		return fmt.Errorf("generate rotated project key: %w", err)
	}
	environments, err := client.ListEnvironments(slug)
	if err != nil {
		return fmt.Errorf("list environments: %w", err)
	}
	for _, environment := range environments {
		envName := environment
		remote, pullErr := client.Pull(slug, envName)
		if pullErr != nil {
			continue
		}
		plaintext, decryptErr := cliCrypto.DecryptEnvFileWithProjectKey(remote.EncryptedData, remote.Nonce, oldKey, slug, envName)
		if decryptErr != nil {
			return fmt.Errorf("decrypt %s before key rotation: %w", envName, decryptErr)
		}
		ciphertext, nonce, encryptErr := cliCrypto.EncryptEnvFileWithProjectKey(plaintext, newKey, slug, envName)
		if encryptErr != nil {
			return fmt.Errorf("encrypt %s with rotated key: %w", envName, encryptErr)
		}
		signature, _, publicKey, signErr := ensureIdentityAndSign(ciphertext)
		if signErr != nil {
			return fmt.Errorf("sign rotated %s: %w", envName, signErr)
		}
		if _, pushErr := client.Push(slug, envName, api.PushRequest{
			EncryptedData: ciphertext, Nonce: nonce, Signature: signature,
			SignerPubKey: identity.Hex(publicKey), BaseVersion: remote.Version,
		}); pushErr != nil {
			return fmt.Errorf("push rotated %s: %w", envName, pushErr)
		}
	}
	members, err := client.ListTeamMembers(slug)
	if err != nil {
		return fmt.Errorf("list remaining members: %w", err)
	}
	envelopes := make(map[string]api.KeyEnvelope)
	for _, member := range members {
		memberUsername, _ := member["username"].(string)
		if memberUsername == "" || strings.EqualFold(memberUsername, removedUsername) {
			continue
		}
		publicKeyHex, _ := member["public_key"].(string)
		publicKey, decodeErr := hex.DecodeString(publicKeyHex)
		if decodeErr != nil || len(publicKey) != dotsyncCrypto.X25519KeySize {
			return fmt.Errorf("remaining member @%s has no valid encryption key", memberUsername)
		}
		envelope, wrapErr := dotsyncCrypto.WrapProjectKey(slug, newKey, publicKey)
		if wrapErr != nil {
			return fmt.Errorf("wrap rotated key for @%s: %w", memberUsername, wrapErr)
		}
		envelopes[memberUsername] = api.KeyEnvelope{
			RecipientPubKey: envelope.RecipientPubKey, EphemeralPubKey: envelope.EphemeralPubKey,
			Nonce: envelope.Nonce, Ciphertext: envelope.Ciphertext,
		}
	}
	if err := client.RotateAndRemoveTeamMember(slug, removedUsername, envelopes); err != nil {
		return err
	}
	if err := identity.SaveProjectKey(slug, newKey); err != nil {
		return fmt.Errorf("save rotated project key: %w", err)
	}
	return nil
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
			if err := requireReauth(client, "sensitive"); err != nil {
				return err
			}
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
