package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
	cliCrypto "github.com/Pruthviraj36/dotsync/cli/crypto"
)

func rollbackCmd() *cobra.Command {
	var envFlag string
	var outputFlag string
	var forceFlag bool

	cmd := &cobra.Command{
		Use:   "rollback <version>",
		Short: "Roll back to a previous secret version",
		Long: `Downloads a specific historical version of secrets, decrypts it,
and re-uploads it as the new latest version. This creates a new version
entry in history (v_current+1) rather than deleting existing ones —
history is always append-only and immutable.

To see available versions: dotsync history

If you just want to inspect an old version without pushing it:
  dotsync pull --version 3 --output .env.old`,
		Args: cobra.ExactArgs(1),
		Example: `  dotsync rollback 3
  dotsync rollback 3 --env production
  dotsync rollback 3 --dry-run`,
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

			password, err := config.GetProjectPassword(projCfg.ProjectSlug)
			if err != nil {
				return err
			}

			client := api.New(cfg)

			// Fetch current version so we can show what we're rolling back from
			currentVersion, _, _ := client.GetLatestVersion(projCfg.ProjectSlug, env)

			if version == currentVersion {
				fmt.Printf(green("✅ Already at version %d — nothing to roll back.")+"\n", version)
				return nil
			}

			fmt.Printf("\n"+bold("⏮  Rolling back %s/%s")+"\n", projCfg.ProjectSlug, env)
			fmt.Printf("   Current  : "+dim("v%d")+"\n", currentVersion)
			fmt.Printf("   Target   : "+cyan("v%d")+"\n", version)
			fmt.Println()

			// Fetch the historical version
			fmt.Printf(dim("⏳ Fetching v%d...")+"\n", version)
			old, err := client.PullVersion(projCfg.ProjectSlug, env, version)
			if err != nil {
				return fmt.Errorf("could not fetch version %d: %w", version, err)
			}

			// Decrypt it to show the user what they're rolling back to
			plaintext, err := cliCrypto.DecryptEnvFile(
				old.EncryptedData, old.Nonce, password, projCfg.ProjectSlug,
			)
			if err != nil {
				return fmt.Errorf("could not decrypt v%d: %w", version, err)
			}

			parsed := cliCrypto.ParseEnvFile(plaintext)
			fmt.Printf("   Contains : %d secrets (pushed by @%s)\n\n", len(parsed), old.PushedBy)

			// Write to a temp preview file if specified
			if outputFlag != "" {
				if err := os.WriteFile(outputFlag, []byte(plaintext), 0600); err != nil {
					return err
				}
				fmt.Printf(green("✅ Version %d written to %s (not pushed — inspect before committing)\n",
					version, outputFlag)
				return nil
			}

			// Confirm before pushing
			if !forceFlag {
				fmt.Printf("Keys in v%d:\n", version)
				keys := make([]string, 0, len(parsed))
				for k := range parsed {
					keys = append(keys, k)
				}
				for _, k := range keys {
					fmt.Printf("  • "+cyan("%s")+"\n", k)
				}
				fmt.Println()
				fmt.Printf("Re-encrypt v%d and push as v%d? [y/N]: ",
					version, currentVersion+1)
				var confirm string
				fmt.Scanln(&confirm)
				if confirm != "y" && confirm != "Y" {
					fmt.Println("Aborted. Nothing was changed.")
					return nil
				}
			}

			// Re-encrypt with the same project password and push as a new version
			// This is important: we don't just re-upload the old ciphertext because
			// re-encrypting generates a fresh nonce (AES-GCM nonce reuse is catastrophic).
			fmt.Print(dim("🔒 Re-encrypting and pushing..."))

			ciphertext, nonce, err := cliCrypto.EncryptEnvFile(plaintext, password, projCfg.ProjectSlug)
			if err != nil {
				fmt.Println(" ❌")
				return fmt.Errorf("re-encryption failed: %w", err)
			}

			result, err := client.Push(projCfg.ProjectSlug, env, api.PushRequest{
				EncryptedData: ciphertext,
				Nonce:         nonce,
			})
			if err != nil {
				fmt.Println(" ❌")
				return err
			}

			fmt.Println(green(" ✅"))
			fmt.Println()
			fmt.Printf("  "+green("✅ Rolled back successfully")+"\n")
			fmt.Printf("  "+bold("Project")+"  : %s\n", projCfg.ProjectSlug)
			fmt.Printf("  "+bold("Env")+"      : %s\n", env)
			fmt.Printf("  "+bold("Restored")+": "+green("v%d content")+"\n", version)
			fmt.Printf("  "+bold("New ver")+"  : "+green("v%d")+"\n", result.Version)
			fmt.Println()
			fmt.Println("  History preserved — v" +
				fmt.Sprint(version) + " through v" +
				fmt.Sprint(currentVersion) + " still accessible via 'dotsync history'")
			fmt.Println()

			// Also update local .env if it exists
			if _, err := os.Stat(".env"); err == nil {
				fmt.Print("  Update local .env? [Y/n]: ")
				var confirm string
				fmt.Scanln(&confirm)
				if confirm == "" || strings.ToLower(confirm) == "y" {
					os.WriteFile(".env", []byte(plaintext), 0600)
					fmt.Println("  "+green("✅ Local .env updated"))
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	cmd.Flags().StringVarP(&outputFlag, "output", "o", "",
		"write to file instead of pushing (inspect before committing)")
	cmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "skip confirmation prompt")
	return cmd
}
