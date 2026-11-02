package cmd

import (
	"fmt"
	"os"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func requireReauth(client *api.Client, scope string) error {
	password, err := readPassword(" Account password: ")
	if err != nil {
		return err
	}
	if err := client.Reauthenticate(password, scope); err != nil {
		return fmt.Errorf("account password confirmation failed: %w", err)
	}
	return nil
}

func accountPasswordCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account-password",
		Short: "Set or change your DotSync account password",
	}
	cmd.AddCommand(&cobra.Command{
		Use:  "set",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireLogin()
			if err != nil {
				return err
			}
			current, err := readOptionalPassword(" Current account password (leave empty if not set): ")
			if err != nil {
				return err
			}
			next, err := readPassword(" New account password: ")
			if err != nil {
				return err
			}
			confirm, err := readPassword(" Confirm account password: ")
			if err != nil {
				return err
			}
			if next != confirm {
				return fmt.Errorf("passwords do not match")
			}
			if err := api.New(cfg).SetAccountPassword(current, next); err != nil {
				return fmt.Errorf("save account password: %w", err)
			}
			complete("Account password saved. It cannot be reset automatically.")
			return nil
		},
	})
	return cmd
}

func readOptionalPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		password, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		return string(password), nil
	}
	return "", nil
}
