package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/yourusername/dotsync/cli/api"
	cliCrypto "github.com/yourusername/dotsync/cli/crypto"
	"github.com/yourusername/dotsync/cli/config"
)

func pushCmd() *cobra.Command {
	var envFlag string
	var fileFlag string

	cmd := &cobra.Command{
		Use:   "push",
		Short: "Encrypt and upload your .env to DotSync",
		Long: `Reads your .env file, encrypts it locally with AES-256-GCM,
and uploads the encrypted blob. The server never sees your raw secrets.`,
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

			// Count keys for display
			parsed := cliCrypto.ParseEnvFile(string(data))
			keyCount := len(parsed)

			fmt.Printf("🔒 Encrypting %d secrets for %s/%s...\n",
				keyCount, projCfg.ProjectSlug, env)

			// Client-side AES-256-GCM encryption
			ciphertext, nonce, err := cliCrypto.EncryptEnvFile(
				string(data), cfg.AccessToken, projCfg.ProjectSlug,
			)
			if err != nil {
				return fmt.Errorf("encryption failed: %w", err)
			}

			fmt.Print("📤 Uploading...")

			client := api.New(cfg)
			result, err := client.Push(projCfg.ProjectSlug, env, api.PushRequest{
				EncryptedData: ciphertext,
				Nonce:         nonce,
			})
			if err != nil {
				fmt.Println(" ❌")
				return err
			}

			fmt.Println(" ✅")
			fmt.Println()
			fmt.Printf("  Project : %s\n", projCfg.ProjectSlug)
			fmt.Printf("  Env     : %s\n", env)
			fmt.Printf("  Version : v%d\n", result.Version)
			fmt.Printf("  Secrets : %d keys encrypted\n", keyCount)
			fmt.Println()
			fmt.Println("  Teammates can now run: dotsync pull")
			fmt.Println()

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
		Long: `Downloads the latest encrypted secret blob from DotSync,
decrypts it locally with AES-256-GCM, and writes your .env file.`,
		Example: `  dotsync pull
  dotsync pull --env production
  dotsync pull --env staging --output .env.staging
  dotsync pull --force  # overwrite without confirmation`,
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

			fmt.Printf("📥 Fetching secrets for %s/%s...\n", projCfg.ProjectSlug, env)

			client := api.New(cfg)
			result, err := client.Pull(projCfg.ProjectSlug, env)
			if err != nil {
				return err
			}

			fmt.Print("🔓 Decrypting...")

			// Client-side decryption
			plaintext, err := cliCrypto.DecryptEnvFile(
				result.EncryptedData, result.Nonce,
				cfg.AccessToken, projCfg.ProjectSlug,
			)
			if err != nil {
				fmt.Println(" ❌")
				return err
			}

			fmt.Println(" ✅")

			// Write with secure permissions (owner read/write only)
			if err := os.WriteFile(outputFile, []byte(plaintext), 0600); err != nil {
				return fmt.Errorf("write %s: %w", outputFile, err)
			}

			parsed := cliCrypto.ParseEnvFile(plaintext)

			fmt.Println()
			fmt.Printf("  Project  : %s\n", projCfg.ProjectSlug)
			fmt.Printf("  Env      : %s\n", env)
			fmt.Printf("  Version  : v%d\n", result.Version)
			fmt.Printf("  Pushed by: %s\n", result.PushedBy)
			fmt.Printf("  Secrets  : %d keys written to %s\n", len(parsed), outputFile)
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	cmd.Flags().StringVarP(&outputFlag, "output", "o", "", "output file path (default: .env)")
	cmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "overwrite without confirmation")
	return cmd
}
