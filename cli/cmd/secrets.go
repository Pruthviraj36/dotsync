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
		Long: `Reads your .env file, encrypts it locally with AES-256-GCM,
and uploads the ciphertext. The server never sees your raw secrets.`,
		Example: `  dotsync push
  dotsync push --env production
  dotsync push --file .env.staging --env staging`,
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

			var password string
			if localFlag {
				password = cfg.AccessToken
			} else {
				password, err = resolvePassword(client, projCfg.ProjectSlug)
				if err != nil {
					return err
				}
			}

			fmt.Println(prog("Encrypting", boldCyan(projCfg.ProjectSlug+"/"+env)+" — "+dim(fmt.Sprintf("%d secrets", len(keys)))))

			ciphertext, nonce, err := cliCrypto.EncryptEnvFile(
				string(data), password, projCfg.ProjectSlug,
			)
			if err != nil {
				return fmt.Errorf("encryption failed: %w", err)
			}

			signature, identityCreated, pub, err := ensureIdentityAndSign(ciphertext)
			if err != nil {
				return err
			}
			if identityCreated {
				fmt.Println(info("ed25519 identity created: " + identity.PubKeyPath()))
			}

			if err := client.SetPubKey(identity.Hex(pub)); err != nil {
				fmt.Println(dim("  (could not sync public key — signature may not verify for teammates yet)"))
			}

			rev := revString(digestOf(ciphertext))
			fmt.Println(prog("Uploading", dim(humanSize(len(ciphertext)))))

			result, err := client.Push(projCfg.ProjectSlug, env, api.PushRequest{
				EncryptedData: ciphertext,
				Nonce:         nonce,
				Signature:     signature,
			})
			if err != nil {
				return err
			}

			blank()
			fmt.Println(ok(boldCyan(projCfg.ProjectSlug+"/"+env)+" "+dim("→")+" "+green(fmt.Sprintf("v%d", result.Version))))
			blank()
			kv("Project", projCfg.ProjectSlug)
			kv("Env", env)
			kvGreen("Version", fmt.Sprintf("v%d", result.Version))
			kvGreen("Secrets", fmt.Sprintf("%d keys encrypted", len(keys)))
			kvDim("Rev", rev)
			kvDim("Size", humanSize(len(ciphertext)))
			blank()
			if localFlag {
				hint("Pull with: dotsync pull --local")
			} else {
				hint("teammates can run: dotsync pull")
			}
			blank()
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
  dotsync pull --force`,
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

			if !forceFlag {
				if _, err := os.Stat(outputFile); err == nil {
					fmt.Printf("%s  %s already exists. Overwrite? [y/N]: ",
						warn(""), outputFile)
					var confirm string
					fmt.Scanln(&confirm)
					if confirm != "y" && confirm != "Y" {
						fmt.Println(dim("  Aborted."))
						return nil
					}
				}
			}

			fmt.Println(prog("Fetching", boldCyan(projCfg.ProjectSlug)+"/"+cyan(env)))

			client := api.New(cfg)
			result, err := client.Pull(projCfg.ProjectSlug, env)
			if err != nil {
				return err
			}

			if verifyFlag {
				verified, verifyErr := verifySignature(
					result.EncryptedData, result.Signature, result.PushedByPubKey,
				)
				if verifyErr != nil {
					return fmt.Errorf(
						"signature verification failed: %w\nRefusing to write %s",
						verifyErr, outputFile,
					)
				}
				switch {
				case verified:
					fmt.Println(ok(fmt.Sprintf("Signature verified — pushed by @%s (ed25519)", result.PushedBy)))
				case len(result.Signature) == 0:
					fmt.Println(dim("  No signature (pushed before signing was enabled)"))
				default:
					fmt.Printf(dim("  No public key on file for @%s — signature unverified")+"\n", result.PushedBy)
				}
			}

			fmt.Println(prog("Decrypting", dim("AES-256-GCM...")))

			var password string
			if localFlag {
				password = cfg.AccessToken
			} else {
				password, err = resolvePassword(client, projCfg.ProjectSlug)
				if err != nil {
					return err
				}
			}

			plaintext, err := cliCrypto.DecryptEnvFile(
				result.EncryptedData, result.Nonce,
				password, projCfg.ProjectSlug,
			)
			if err != nil {
				return err
			}

			if err := os.WriteFile(outputFile, []byte(plaintext), 0600); err != nil {
				return fmt.Errorf("write %s: %w", outputFile, err)
			}

			parsed := cliCrypto.ParseEnvFile(plaintext)
			rev := revString(digestOf(result.EncryptedData))

			blank()
			fmt.Println(ok(cyan(outputFile)+" "+dim(fmt.Sprintf("← %d secrets decrypted", len(parsed)))))
			blank()
			kv("Project", projCfg.ProjectSlug)
			kv("Env", env)
			kvGreen("Version", fmt.Sprintf("v%d", result.Version))
			kvCyan("Pushed by", "@"+result.PushedBy)
			kvDim("Rev", rev)
			blank()
			hint("To see what changed: dotsync diff")
			blank()
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
