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
	"golang.org/x/term"
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
			keys, err := cliCrypto.ParseEnvFileStrict(string(data))
			if err != nil {
				return fmt.Errorf("%s: %w", envFile, err)
			}
			if len(keys) == 0 {
				return fmt.Errorf("%s contains no KEY=VALUE pairs", envFile)
			}

			password, err := resolvePassword(client, projCfg.ProjectSlug)
			if err != nil {
				return err
			}

			banner(fmt.Sprintf("Push Secrets — %s/%s", projCfg.ProjectSlug, env))
			txRunning("transaction")

			txStep(1, 3, fmt.Sprintf("Encrypting %d secrets with AES-256-GCM", len(keys)))

			ciphertext, nonce, err := cliCrypto.EncryptEnvFileForEnv(
				string(data), password, projCfg.ProjectSlug, env,
			)
			if err != nil {
				return fmt.Errorf("encryption failed: %w", err)
			}

			signature, identityCreated, pub, err := ensureIdentityAndSign(ciphertext)
			if err != nil {
				return err
			}
			if identityCreated {
				fmt.Println("  " + info("ed25519 identity created: "+identity.PubKeyPath()))
			}
			if err := client.SetPubKey(identity.Hex(pub)); err != nil {
				fmt.Println("  " + warn(fmt.Sprintf("public key sync warning: %v", err)))
			}

			txStep(2, 3, "Verifying and attaching ed25519 signature")
			transferLabel := fmt.Sprintf("Uploading %s", humanSize(len(ciphertext)))
			transferProgress(transferLabel, len(ciphertext), 0)

			result, err := client.Push(projCfg.ProjectSlug, env, api.PushRequest{
				EncryptedData: ciphertext,
				Nonce:         nonce,
				Signature:     signature,
				SignerPubKey:  identity.Hex(pub),
			})
			if err != nil {
				if isTTY {
					fmt.Println()
				}
				return err
			}
			transferProgress(transferLabel, len(ciphertext), len(ciphertext))
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
				fmt.Println("  " + warn("could not save pull state: "+err.Error()))
			}

			rev := revString(digestOf(ciphertext))

			transactionSummary("Transaction Summary", [][2]string{
				{"Operation", "Push"},
				{"Project", projCfg.ProjectSlug},
				{"Environment", env},
				{"Version", fmt.Sprintf("v%d", result.Version)},
				{"Secrets", fmt.Sprintf("%d encrypted", len(keys))},
				{"Payload Size", humanSize(len(ciphertext))},
				{"Revision", rev},
			})

			complete(fmt.Sprintf("Pushed %s/%s as v%d", projCfg.ProjectSlug, env, result.Version))
			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	cmd.Flags().StringVarP(&fileFlag, "file", "f", "", "path to .env file (default: .env)")
	return cmd
}

func pullCmd() *cobra.Command {
	var envFlag string
	var outputFlag string
	var forceFlag bool

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
							who = " by @" + latestBy
						}
						fmt.Printf("%s is already up to date (version v%d%s).\n", outputFile, latestVersion, who)
						fmt.Println("Nothing to do.")
						complete("")
						return nil
					}
				}
			}

			if !forceFlag {
				if _, err := os.Stat(outputFile); err == nil {
					if !term.IsTerminal(int(os.Stdin.Fd())) {
						return fmt.Errorf("%s already exists and there is no interactive terminal — re-run with --force to overwrite", outputFile)
					}
					fmt.Printf("%s already exists. Overwrite? [y/N]: ", outputFile)
					var confirm string
					fmt.Scanln(&confirm)
					if confirm != "y" && confirm != "Y" {
						fmt.Println(dim("Operation aborted."))
						return fmt.Errorf("pull cancelled — pass --force to overwrite %s", outputFile)
					}
				}
			}

			banner(fmt.Sprintf("Pull Secrets — %s/%s", projCfg.ProjectSlug, env))
			txRunning("transaction")
			transferLabel := fmt.Sprintf("Fetching %s/%s", projCfg.ProjectSlug, env)
			transferProgress(transferLabel, 1, 0)

			result, err := client.Pull(projCfg.ProjectSlug, env)
			if err != nil {
				if isTTY {
					fmt.Println()
				}
				return err
			}
			transferProgress(transferLabel, len(result.EncryptedData), len(result.EncryptedData))

			// Always verify signatures if present; warn if unsigned
			verified, verifyErr := verifySignature(
				result.EncryptedData, result.Signature, result.PushedByPubKey,
			)
			if verifyErr != nil {
				return fmt.Errorf("signature verification failed: %w\nrefusing to write %s", verifyErr, outputFile)
			}
			if verified {
				fmt.Println("  " + info(fmt.Sprintf("signature verified (@%s, ed25519)", result.PushedBy)))
			} else if len(result.Signature) == 0 {
				fmt.Println("  " + warn("secret version is not signed (pushed before signature verification was enabled)"))
			}

			txStep(2, 3, "Verifying ed25519 push signature")

			password, err := resolvePassword(client, projCfg.ProjectSlug)
			if err != nil {
				return err
			}

			plaintext, err := cliCrypto.DecryptEnvFileForEnv(
				result.EncryptedData, result.Nonce,
				password, projCfg.ProjectSlug, env,
			)
			if err != nil {
				return err
			}

			if err := writeFileAtomic(outputFile, []byte(plaintext), 0600); err != nil {
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
				fmt.Println("  " + warn("could not save pull state: "+err.Error()))
			}

			txStep(3, 3, fmt.Sprintf("Decrypting AES-256-GCM and writing %s", outputFile))

			parsed := cliCrypto.ParseEnvFile(plaintext)
			rev := revString(digestOf(result.EncryptedData))

			transactionSummary("Transaction Summary", [][2]string{
				{"Operation", "Pull"},
				{"Project", projCfg.ProjectSlug},
				{"Environment", env},
				{"Version", fmt.Sprintf("v%d", result.Version)},
				{"Secrets", fmt.Sprintf("%d decrypted", len(parsed))},
				{"Output File", outputFile},
				{"Pushed by", "@" + result.PushedBy},
				{"Revision", rev},
			})

			complete(fmt.Sprintf("Synchronized %s (v%d)", outputFile, result.Version))
			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	cmd.Flags().StringVarP(&outputFlag, "output", "o", "", "output file path (default: .env)")
	cmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "overwrite without confirmation")
	return cmd
}
