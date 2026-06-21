package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func updateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update DotSync to the latest version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("⏳ Checking for updates...")
			
			// Try to update using go install
			_, err := exec.LookPath("go")
			if err != nil {
				fmt.Println("❌ Go toolchain not found.")
				fmt.Println("Please download the latest release from: https://github.com/Pruthviraj36/dotsync/releases")
				return nil
			}

			fmt.Println("🔄 Running: go install github.com/Pruthviraj36/dotsync@latest")
			
			c := exec.Command("go", "install", "github.com/Pruthviraj36/dotsync@latest")
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			
			if err := c.Run(); err != nil {
				return fmt.Errorf("update failed: %w", err)
			}

			fmt.Println("✅ DotSync updated successfully!")
			return nil
		},
	}
}
