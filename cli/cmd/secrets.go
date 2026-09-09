package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
	cliCrypto "github.com/Pruthviraj36/dotsync/cli/crypto"
	"github.com/Pruthviraj36/dotsync/cli/identity"
	"github.com/spf13/cobra"
)

func localPullDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func localPullStateKey(env, outputFile string) string {
	return env + "\x00" + outputFile
}

func pushCmd() *cobra.Command {
	var envFlag string
	var fileFlag string
	var localFlag bool

	cmd := &cobra.Command{
		Use:   "push",
		Short: "Encrypt and upload your .env to DotSync",
		Long: `Reads your .env, encrypts it on this machine with AES-256-GCM,
and uploads the ciphertext. The server never sees plaintext.`,
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
					return fmt.Errorf("no %s found\n%screate one first", envFile, msgPad())
				}
				return fmt.Errorf("read %s: %w", envFile, err)
			}
			if len(data) == 0 {
				return fmt.Errorf("%s is empty", envFile)
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

			fmt.Println(prog("Encrypting", boldCyan(projCfg.ProjectSlug+"/"+env)+dim(fmt.Sprintf("  %d secrets", len(keys)))))

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
				fmt.Println(info("ed25519 identity created · " + dim(identity.PubKeyPath())))
			}
			if err := client.SetPubKey(identity.Hex(pub)); err != nil {
				fmt.Println(dim(msgPad() + "(public key sync failed — will retry)"))
			}

			fmt.Println(prog("Uploading", dim(humanSize(len(ciphertext)))))

			result, err := client.Push(projCfg.ProjectSlug, env, api.PushRequest{
				EncryptedData: ciphertext,
				Nonce:         nonce,
				Signature:     signature,
			})
			if err != nil {
				return err
			}
			if projCfg.LastPulledStates == nil {
				projCfg.LastPulledStates = make(map[string]config.PulledState)
			}
			// A successful push already leaves the workspace at the server head,
			// so the next pull can take the same no-op path as a prior pull.
			projCfg.LastPulledStates[localPullStateKey(env, envFile)] = config.PulledState{
				Version: result.Version,
				Digest:  localPullDigest(data),
			}
			if err := config.SaveProject(projCfg); err != nil {
				fmt.Println(warn(dim("could not save pull state: " + err.Error())))
			}

			rev := revString(digestOf(ciphertext))

			blank()
			fmt.Println(ok(boldCyan(projCfg.ProjectSlug+"/"+env) + "  " + green(fmt.Sprintf("v%d", result.Version))))
			blank()
			kv("project", projCfg.ProjectSlug)
			kv("env", env)
			kvGreen("version", fmt.Sprintf("v%d", result.Version))
			kvGreen("secrets", fmt.Sprintf("%d encrypted", len(keys)))
			kvDim("size", humanSize(len(ciphertext)))
			kvDim("rev", rev)
			blank()
			hint("teammates can run: dotsync pull")
			blank()
			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	cmd.Flags().StringVarP(&fileFlag, "file", "f", "", "path to .env file (default: .env)")
	cmd.Flags().BoolVar(&localFlag, "local", false, "encrypt with personal token instead of team password")
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
		Long: `Checks the remote version first, then downloads and decrypts only
when the environment changed (or the local file was edited). The server
never sees plaintext.`,
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

			client := api.New(cfg)
			// A pull first asks for the lightweight history head. We only fetch and
			// decrypt the payload when the remote changed or the local file was
			// edited outside DotSync. --force always performs a full pull.
			if !forceFlag {
				latestVersion, latestBy, err := client.GetLatestVersion(projCfg.ProjectSlug, env)
				if err != nil {
					return fmt.Errorf("check remote version: %w", err)
				}
				state, known := projCfg.LastPulledStates[localPullStateKey(env, outputFile)]
				if known && latestVersion > 0 && state.Version == latestVersion {
					if data, readErr := os.ReadFile(outputFile); readErr == nil && localPullDigest(data) == state.Digest {
						who := ""
						if latestBy != "" {
							who = " · @" + latestBy
						}
						fmt.Printf("%s%s already up to date · v%d%s\n", msgPad(), outputFile, latestVersion, who)
						return nil
					}
				}
			}

			if !forceFlag {
				if _, err := os.Stat(outputFile); err == nil {
					fmt.Printf("%s%s already exists. Overwrite? [y/N]: ", msgPad(), outputFile)
					var confirm string
					fmt.Scanln(&confirm)
					if confirm != "y" && confirm != "Y" {
						fmt.Println(dim(msgPad() + "aborted"))
						return nil
					}
				}
			}

			fmt.Println(prog("Fetching", boldCyan(projCfg.ProjectSlug+"/"+env)))

			result, err := client.Pull(projCfg.ProjectSlug, env)
			if err != nil {
				return err
			}

			// Always verify signatures if present; warn if unsigned
			verified, verifyErr := verifySignature(
				result.EncryptedData, result.Signature, result.PushedByPubKey,
			)
			if verifyErr != nil {
				return fmt.Errorf("signature verification failed: %w\nrefusing to write %s", verifyErr, outputFile)
			}
			if verified {
				fmt.Println(info(dim("signature verified · @" + result.PushedBy + " (ed25519)")))
			} else if len(result.Signature) == 0 {
				fmt.Println(warn(dim("⚠️  warning: this secret version is not signed (pushed before signature verification was enabled)")))
				fmt.Println(dim(msgPad() + "for security, consider re-pushing with: dotsync push --env " + env))
			}

			fmt.Println(prog("Decrypting", dim("AES-256-GCM")))

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
			if projCfg.LastPulledStates == nil {
				projCfg.LastPulledStates = make(map[string]config.PulledState)
			}
			projCfg.LastPulledStates[localPullStateKey(env, outputFile)] = config.PulledState{
				Version: result.Version,
				Digest:  localPullDigest([]byte(plaintext)),
			}
			if err := config.SaveProject(projCfg); err != nil {
				fmt.Println(warn(dim("could not save pull state: " + err.Error())))
			}

			parsed := cliCrypto.ParseEnvFile(plaintext)
			rev := revString(digestOf(result.EncryptedData))

			blank()
			fmt.Println(ok(cyan(outputFile) + dim(fmt.Sprintf("  %d secrets", len(parsed)))))
			blank()
			kv("project", projCfg.ProjectSlug)
			kv("env", env)
			kvGreen("version", fmt.Sprintf("v%d", result.Version))
			kvDim("pushed by", "@"+result.PushedBy)
			kvDim("rev", rev)
			blank()
			hint("dotsync diff  — see what changed")
			blank()
			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	cmd.Flags().StringVarP(&outputFlag, "output", "o", "", "output file path (default: .env)")
	cmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "overwrite without confirmation")
	cmd.Flags().BoolVar(&localFlag, "local", false, "decrypt with personal token instead of team password")
	cmd.Flags().BoolVar(&verifyFlag, "verify", true, "verify ed25519 push signature")
	return cmd
}
