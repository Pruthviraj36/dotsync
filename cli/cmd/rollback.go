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
	var allowUnsignedFlag bool

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

			banner(fmt.Sprintf("Rollback · %s/%s", projCfg.ProjectSlug, env))
			txRunning("rollback preparation")

			txStep(1, 2, fmt.Sprintf("Fetching v%d payload", version))
			old, err := client.PullVersion(projCfg.ProjectSlug, env, version)
			if err != nil {
				return fmt.Errorf("could not fetch v%d: %w", version, err)
			}

			if verified, vErr := verifySignature(old.EncryptedData, old.Signature, old.PushedByPubKey); vErr != nil {
				return fmt.Errorf("signature verification failed: %w\nRefusing to roll back to an unverified version", vErr)
			} else if !verified {
				if !allowUnsignedFlag && !forceFlag {
					return fmt.Errorf("v%d is not signed (legacy secret pushed before signature verification was enabled)\nUse --allow-unsigned to proceed with rollback", version)
				}
				fmt.Println(warn(fmt.Sprintf("v%d is unsigned (proceeding per --allow-unsigned)", version)))
			} else {
				txStep(2, 2, fmt.Sprintf("Verifying signature (@%s ed25519)", old.PushedBy))
			}

			plaintext, err := cliCrypto.DecryptEnvFile(
				old.EncryptedData, old.Nonce, password, projCfg.ProjectSlug,
			)
			if err != nil {
				return fmt.Errorf("could not decrypt v%d: %w", version, err)
			}

			parsed := cliCrypto.ParseEnvFile(plaintext)

			// Inspect mode — write to file, don't push
			if outputFlag != "" {
				if err := writeFileAtomic(outputFlag, []byte(plaintext), 0600); err != nil {
					return err
				}
				blank()
				fmt.Println(ok(fmt.Sprintf("v%d decrypted and written to %s", version, outputFlag)))
				hint("Inspect the file, then push it manually if it looks right:")
				cmdHint(fmt.Sprintf("cp %s .env && dotsync push --env %s", outputFlag, env))
				complete("")
				return nil
			}

			// Confirmation prompt
			if !forceFlag {
				blank()
				var rows [][]string
				for k := range parsed {
					rows = append(rows, []string{k, fmt.Sprintf("Restore from v%d", version)})
				}
				table([]string{"Secret Key", "Action"}, rows)

				fmt.Printf("Re-encrypt v%d (%d secrets) and push as v%d? [y/N]: ",
					version, len(parsed), currentVersion+1)
				var confirm string
				fmt.Scanln(&confirm)
				if confirm != "y" && confirm != "Y" {
					fmt.Println(dim("Aborted — nothing changed."))
					blank()
					return nil
				}
			}

			blank()
			txRunning("rollback transaction")
			txStep(1, 3, fmt.Sprintf("Re-encrypting %d secret(s) with fresh nonce", len(parsed)))
			ciphertext, nonce, err := cliCrypto.EncryptEnvFile(plaintext, password, projCfg.ProjectSlug)
			if err != nil {
				return fmt.Errorf("re-encryption failed: %w", err)
			}

			signature, identityCreated, pub, err := ensureIdentityAndSign(ciphertext)
			if err != nil {
				return err
			}
			if identityCreated {
				fmt.Println(info("Created new ed25519 identity: " + identity.PubKeyPath()))
			}
			if err := client.SetPubKey(identity.Hex(pub)); err != nil {
				// non-fatal
			}

			txStep(2, 3, "Signing payload with local ed25519 key")
			txStep(3, 3, fmt.Sprintf("Uploading payload (%s)", humanSize(len(ciphertext))))

			result, err := client.Push(projCfg.ProjectSlug, env, api.PushRequest{
				EncryptedData: ciphertext,
				Nonce:         nonce,
				Signature:     signature,
				SignerPubKey:  identity.Hex(pub),
			})
			if err != nil {
				return err
			}

			transactionSummary("Rollback Summary", [][2]string{
				{"Project", projCfg.ProjectSlug},
				{"Environment", env},
				{"Restored From", fmt.Sprintf("v%d", version)},
				{"New Version", fmt.Sprintf("v%d", result.Version)},
				{"Secrets Count", fmt.Sprintf("%d", len(parsed))},
				{"Size", humanSize(len(ciphertext))},
			})

			complete(fmt.Sprintf("%s/%s rolled back to v%d content as v%d", projCfg.ProjectSlug, env, version, result.Version))

			// Offer to update local .env
			if _, err := os.Stat(".env"); err == nil {
				fmt.Printf("Update local .env file? [Y/n]: ")
				var confirm string
				fmt.Scanln(&confirm)
				if confirm == "" || strings.ToLower(confirm) == "y" {
					_ = writeFileAtomic(".env", []byte(plaintext), 0600)
					fmt.Println(ok(".env updated."))
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	cmd.Flags().StringVarP(&outputFlag, "output", "o", "", "write to file instead of pushing")
	cmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "skip confirmation")
	cmd.Flags().BoolVar(&allowUnsignedFlag, "allow-unsigned", false, "allow rolling back to legacy unsigned versions")
	return cmd
}
