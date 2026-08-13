package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
	cliCrypto "github.com/Pruthviraj36/dotsync/cli/crypto"
	"github.com/Pruthviraj36/dotsync/cli/identity"
)

func rollbackCmd() *cobra.Command {
	var envFlag string
	var outputFlag string
	var forceFlag bool

	cmd := &cobra.Command{
		Use:   "rollback <version>",
		Short: "Roll back to a previous secret version",
		Long: `Downloads a specific historical version, decrypts it, and
re-uploads it as the latest version. History is append-only —
nothing is ever deleted. The rolled-back content becomes v_current+1.

To see available versions: dotsync history`,
		Args: cobra.ExactArgs(1),
		Example: `  dotsync rollback 3
  dotsync rollback 3 --env production
  dotsync rollback 3 --output .env.old`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireLogin()
			if err != nil {
				return err
			}
			projCfg, err := config.LoadProject()
			if err != nil {
				return err
			}

			env := envFlag
			if env == "" {
				env = projCfg.DefaultEnv
			}

			var version int
			if _, err := fmt.Sscanf(args[0], "%d", &version); err != nil || version < 1 {
				return fmt.Errorf("version must be a positive integer, got: %s", args[0])
			}

			client := api.New(cfg)
			password, err := resolvePassword(client, projCfg.ProjectSlug)
			if err != nil {
				return err
			}

			currentVersion, _, _ := client.GetLatestVersion(projCfg.ProjectSlug, env)
			if version == currentVersion {
				blank()
				fmt.Println(info(fmt.Sprintf("Already at v%d — nothing to roll back.", version)))
				blank()
				return nil
			}

			blank()
			fmt.Printf("  %s  %s/%s\n", bold("Rollback"), boldCyan(projCfg.ProjectSlug), cyan(env))
			blank()
			kv("Current", fmt.Sprintf("v%d", currentVersion))
			kvCyan("Target", fmt.Sprintf("v%d", version))
			blank()

			fmt.Println(spin(fmt.Sprintf("Fetching v%d...", version)))
			old, err := client.PullVersion(projCfg.ProjectSlug, env, version)
			if err != nil {
				return fmt.Errorf("could not fetch v%d: %w", version, err)
			}

			if verified, vErr := verifySignature(old.EncryptedData, old.Signature, old.PushedByPubKey); vErr != nil {
				return fmt.Errorf("signature verification failed: %w\nRefusing to roll back to an unverified version", vErr)
			} else if verified {
				fmt.Println(ok(fmt.Sprintf("Signature verified — originally pushed by @%s", old.PushedBy)))
			}

			plaintext, err := cliCrypto.DecryptEnvFile(
				old.EncryptedData, old.Nonce, password, projCfg.ProjectSlug,
			)
			if err != nil {
				return fmt.Errorf("could not decrypt v%d: %w", version, err)
			}

			parsed := cliCrypto.ParseEnvFile(plaintext)
			blank()
			kvCyan("Contains", fmt.Sprintf("%d secrets (originally pushed by @%s)", len(parsed), old.PushedBy))
			blank()

			// Inspect mode — write to file, don't push
			if outputFlag != "" {
				if err := os.WriteFile(outputFlag, []byte(plaintext), 0600); err != nil {
					return err
				}
				fmt.Println(ok(fmt.Sprintf("v%d written to %s", version, outputFlag)))
				hint("Inspect the file, then push it manually if it looks right:")
				cmdHint(fmt.Sprintf("cp %s .env && dotsync push --env %s", outputFlag, env))
				blank()
				return nil
			}

			// Confirmation prompt
			if !forceFlag {
				rl := ruleN(48)
				fmt.Println("  " + rl)
				for k := range parsed {
					fmt.Printf("  %s  %s\n", dim("·"), cyan(k))
				}
				fmt.Println("  " + rl)
				blank()
				fmt.Printf("  Re-encrypt v%d content and push as %s? [y/N]: ",
					version, green(fmt.Sprintf("v%d", currentVersion+1)))
				var confirm string
				fmt.Scanln(&confirm)
				if confirm != "y" && confirm != "Y" {
					fmt.Println(dim("  Aborted — nothing changed."))
					blank()
					return nil
				}
			}

			fmt.Println(spin("Re-encrypting with fresh nonce..."))
			ciphertext, nonce, err := cliCrypto.EncryptEnvFile(plaintext, password, projCfg.ProjectSlug)
			if err != nil {
				return fmt.Errorf("re-encryption failed: %w", err)
			}

			signature, identityCreated, pub, err := ensureIdentityAndSign(ciphertext)
			if err != nil {
				return err
			}
			if identityCreated {
				fmt.Println(info("ed25519 identity created: " + identity.PubKeyPath()))
			}
			if err := client.SetPubKey(identity.Hex(pub)); err != nil {
				fmt.Println(dim("  (could not sync public key)"))
			}

			fmt.Println(spin("Pushing..."))
			result, err := client.Push(projCfg.ProjectSlug, env, api.PushRequest{
				EncryptedData: ciphertext,
				Nonce:         nonce,
				Signature:     signature,
			})
			if err != nil {
				return err
			}

			blank()
			fmt.Println(ok("Rolled back successfully"))
			blank()
			kv("Project", projCfg.ProjectSlug)
			kv("Env", env)
			kvGreen("Restored", fmt.Sprintf("v%d content", version))
			kvGreen("New version", fmt.Sprintf("v%d", result.Version))
			kvDim("History", fmt.Sprintf("v1–v%d all still accessible via dotsync history", currentVersion))
			blank()

			// Offer to update local .env
			if _, err := os.Stat(".env"); err == nil {
				fmt.Printf("  Update local .env? [Y/n]: ")
				var confirm string
				fmt.Scanln(&confirm)
				if confirm == "" || strings.ToLower(confirm) == "y" {
					os.WriteFile(".env", []byte(plaintext), 0600)
					fmt.Println(ok("Local .env updated"))
					blank()
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	cmd.Flags().StringVarP(&outputFlag, "output", "o", "", "write to file instead of pushing")
	cmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "skip confirmation")
	return cmd
}
