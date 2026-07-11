package cmd

import (
	"fmt"
	"os"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
	cliCrypto "github.com/Pruthviraj36/dotsync/cli/crypto"
	"github.com/Pruthviraj36/dotsync/cli/identity"
	"github.com/spf13/cobra"
)

func pushCmd() *cobra.Command {
	var envFlag string
	var fileFlag string
	var localFlag bool

	cmd := &cobra.Command{
		Use:   "push",
		Short: "Encrypt and upload your .env to DotSync",
		Long: `Reads your .env file, encrypts it locally,
and uploads the encrypted blob. The server never sees your raw secrets.`,
		Example: `  dotsync push
  dotsync push --env production
  dotsync push --file .env.staging --env staging
  dotsync push --local  # encrypt for personal use only`,
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

			envFile := fileFlag
			if envFile == "" {
				envFile = ".env"
			}

			// Read the .env file
			data, err := os.ReadFile(envFile)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("file not found: %s\n  Create a .env file first.", envFile)
				}
				return fmt.Errorf("read %s: %w", envFile, err)
			}

			if len(data) == 0 {
				return fmt.Errorf("%s is empty — nothing to push", envFile)
			}

			client := api.New(cfg)

			keys := cliCrypto.ParseEnvFile(string(data))

			// Determine encryption password
			var password string
			if localFlag {
				password = cfg.AccessToken
				fmt.Printf(dim("encrypting %d keys …")+"\n", len(keys))
			} else {
				password, err = resolvePassword(client, projCfg.ProjectSlug)
				if err != nil {
					return err
				}
				fmt.Printf(dim("encrypting %d keys …")+"\n", len(keys))
			}

			// Client-side AES-256-GCM encryption
			ciphertext, nonce, err := cliCrypto.EncryptEnvFile(
				string(data), password, projCfg.ProjectSlug,
			)
			if err != nil {
				return fmt.Errorf("encryption failed: %w", err)
			}

			// Sign sha256(ciphertext) with this machine's ed25519 identity —
			// proves *who* pushed, on top of the encryption's own tamper-evidence.
			signature, identityCreated, pub, err := ensureIdentityAndSign(ciphertext)
			if err != nil {
				return err
			}
			if identityCreated {
				fmt.Printf(green("✓ ed25519 identity created — %s")+"\n", identity.PubKeyPath())
			}
			fmt.Println(dim("signing manifest.sig …"))

			// Keep the server's copy of our public key current so teammates
			// can verify this signature. Best-effort: a failure here shouldn't
			// block the push itself.
			if err := client.SetPubKey(identity.Hex(pub)); err != nil {
				fmt.Println(dim("  (could not sync public key — signature may not verify for teammates yet)"))
			}

			rev := revString(digestOf(ciphertext))
			fmt.Printf(dim("uploading ciphertext (%s) …")+"\n", humanSize(len(ciphertext)))

			result, err := client.Push(projCfg.ProjectSlug, env, api.PushRequest{
				EncryptedData: ciphertext,
				Nonce:         nonce,
				Signature:     signature,
			})
			if err != nil {
				return err
			}

			fmt.Println()
			fmt.Printf(green("✓ pushed · rev %s · server sees ciphertext only")+"\n", rev)
			fmt.Println()
			fmt.Printf("  "+bold("Project")+" : %s\n", projCfg.ProjectSlug)
			fmt.Printf("  "+bold("Env")+"     : %s\n", env)
			fmt.Printf("  "+bold("Version")+" : "+green("v%d")+"\n", result.Version)
			fmt.Printf("  "+bold("Secrets")+" : "+green("%d keys encrypted")+"\n", len(keys))
			fmt.Println()
			if localFlag {
				fmt.Println("  You can now run: dotsync pull --local")
			} else {
				fmt.Println("  Teammates can now run: dotsync pull")
			}
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	cmd.Flags().StringVarP(&fileFlag, "file", "f", "", "path to .env file (default: .env)")
	cmd.Flags().BoolVar(&localFlag, "local", false, "encrypt with personal access token instead of team password")
	return cmd
}

func pullCmd() *cobra.Command {
	var envFlag string
	var outputFlag string
	var forceFlag bool
	var localFlag bool
	var verifyFlag bool

	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Download and decrypt latest .env from DotSync",
		Long: `Downloads the latest encrypted secret blob from DotSync,
decrypts it locally, and writes your .env file.`,
		Example: `  dotsync pull
  dotsync pull --env production
  dotsync pull --env staging --output .env.staging
  dotsync pull --force  # overwrite without confirmation
  dotsync pull --local  # decrypt a personal push`,
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

			outputFile := outputFlag
			if outputFile == "" {
				outputFile = ".env"
			}

			// Warn before overwriting
			if !forceFlag {
				if _, err := os.Stat(outputFile); err == nil {
					fmt.Printf("⚠️  %s already exists. Overwrite? [y/N]: ", outputFile)
					var confirm string
					fmt.Scanln(&confirm)
					if confirm != "y" && confirm != "Y" {
						fmt.Println("Aborted.")
						return nil
					}
				}
			}

			client := api.New(cfg)
			result, err := client.Pull(projCfg.ProjectSlug, env)
			if err != nil {
				return err
			}

			// Verify who pushed this, before we even decrypt it. On by
			// default; --verify=false skips it (e.g. for a push made before
			// signing existed on an old server).
			if verifyFlag {
				verified, verifyErr := verifySignature(result.EncryptedData, result.Signature, result.PushedByPubKey)
				if verifyErr != nil {
					return fmt.Errorf("✗ %w\nRefusing to write %s — pull again, and if this keeps happening, tell your team", verifyErr, outputFile)
				}
				switch {
				case verified:
					fmt.Printf(green("✓ signature ok (%s, ed25519)")+"\n", result.PushedBy)
				case len(result.Signature) == 0:
					fmt.Println(dim("  (no signature on this push — pushed before signing was enabled)"))
				default:
					fmt.Printf(dim("  (no public key on file for %s — signature not verified)")+"\n", result.PushedBy)
				}
			}

			var password string
			if localFlag {
				password = cfg.AccessToken
			} else {
				password, err = resolvePassword(client, projCfg.ProjectSlug)
				if err != nil {
					return err
				}
			}

			// Client-side decryption
			plaintext, err := cliCrypto.DecryptEnvFile(
				result.EncryptedData, result.Nonce,
				password, projCfg.ProjectSlug,
			)
			if err != nil {
				return err
			}

			// Write with secure permissions (owner read/write only)
			if err := os.WriteFile(outputFile, []byte(plaintext), 0600); err != nil {
				return fmt.Errorf("write %s: %w", outputFile, err)
			}

			parsed := cliCrypto.ParseEnvFile(plaintext)
			rev := revString(digestOf(result.EncryptedData))

			fmt.Printf(green("✓ decrypted %d keys → %s")+"\n", len(parsed), outputFile)
			fmt.Println(green("✓ integrity verified — nothing tampered"))
			fmt.Println()
			fmt.Printf("  "+bold("Project")+"  : %s\n", projCfg.ProjectSlug)
			fmt.Printf("  "+bold("Env")+"      : %s\n", env)
			fmt.Printf("  "+bold("Version")+"  : "+green("v%d")+" (rev %s)\n", result.Version, rev)
			fmt.Printf("  "+bold("Pushed by")+": "+cyan("%s")+"\n", result.PushedBy)
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	cmd.Flags().StringVarP(&outputFlag, "output", "o", "", "output file path (default: .env)")
	cmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "overwrite without confirmation")
	cmd.Flags().BoolVar(&localFlag, "local", false, "decrypt with personal access token instead of team password")
	cmd.Flags().BoolVar(&verifyFlag, "verify", true, "verify the pusher's ed25519 signature")
	return cmd
}
