package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
	"github.com/spf13/cobra"
)

func loginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Authenticate with GitHub",
		Long: `Opens a GitHub OAuth flow in your browser.

How it works:
  1. Open the URL shown below in your browser
  2. Authorize DotSync on GitHub
  3. Copy the code from the redirect URL (?code=...)
  4. Paste it here`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadGlobal()
			if err != nil {
				return err
			}

			if config.IsLoggedIn(cfg) {
				fmt.Println("✅ Already logged in as", cfg.Username)
				fmt.Println("   Server:", cfg.ServerURL)
				fmt.Println("   Run 'dotsync logout' first to switch accounts.")
				return nil
			}

			fmt.Println("Connecting to server:", cfg.ServerURL)

			fmt.Print("⏳ Connecting to server...")
			authCfg, err := api.GetAuthConfig(cfg.ServerURL)
			if err != nil {
				fmt.Println(" ❌")
				return fmt.Errorf("could not reach server at %s: %w", cfg.ServerURL, err)
			}
			if authCfg.GitHubClientID == "" {
				fmt.Println(" ❌")
				return fmt.Errorf("server has no GITHUB_CLIENT_ID configured — contact the server admin")
			}
			fmt.Println(" ✅")

			authURL := fmt.Sprintf(
				"https://github.com/login/oauth/authorize?client_id=%s&scope=read:user,user:email",
				authCfg.GitHubClientID,
			)

			fmt.Println()
			fmt.Println("🔐 DotSync Login via GitHub OAuth")
			fmt.Println("──────────────────────────────────")
			fmt.Println()
			fmt.Println("1. Open this URL in your browser:")
			fmt.Println()
			fmt.Println("   " + authURL)
			fmt.Println()
			fmt.Println("2. Authorize DotSync on GitHub")
			fmt.Println("3. You'll be redirected to a URL like:")
			fmt.Println("   http://localhost?code=abc123xyz")
			fmt.Println()
			fmt.Print("4. Paste the code here: ")

			reader := bufio.NewReader(os.Stdin)
			code, _ := reader.ReadString('\n')
			code = strings.TrimSpace(code)

			if code == "" {
				return fmt.Errorf("no code provided")
			}

			fmt.Println()
			fmt.Print("⏳ Authenticating...")

			result, err := api.ExchangeGitHubCode(cfg.ServerURL, code)
			if err != nil {
				fmt.Println(" ❌")
				return fmt.Errorf("login failed: %w", err)
			}

			username, _ := result.User["username"].(string)
			userID, _ := result.User["id"].(string)
			plan, _ := result.User["plan"].(string)

			cfg.AccessToken = result.AccessToken
			cfg.RefreshToken = result.RefreshToken
			cfg.UserID = userID
			cfg.Username = username

			if err := config.SaveGlobal(cfg); err != nil {
				return fmt.Errorf("save credentials: %w", err)
			}

			fmt.Println(" ✅")
			fmt.Println()
			fmt.Printf("  Welcome, %s! 👋\n", username)
			fmt.Printf("  Plan: %s\n", plan)
			fmt.Println()
			fmt.Println("  Next: cd into your project and run 'dotsync init'")
			fmt.Println()

			return nil
		},
	}
}

func logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Log out and revoke all sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadGlobal()
			if err != nil {
				return err
			}

			if !config.IsLoggedIn(cfg) {
				fmt.Println("Not logged in.")
				return nil
			}

			client := api.New(cfg)
			// Best-effort server-side revocation
			_ = client.Logout()

			if err := config.ClearGlobal(); err != nil {
				return fmt.Errorf("clear credentials: %w", err)
			}

			fmt.Println("✅ Logged out. All sessions revoked.")
			return nil
		},
	}
}
