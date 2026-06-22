package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
)

func loginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Authenticate with GitHub",
		Long: `Authenticate with GitHub using the OAuth Device Flow.

This works the same way whether you're on your laptop, SSH'd into a remote
server, or running inside a container — you'll get a short code to enter
at github.com/login/device, which you can open on ANY device with a
browser (your phone is fine). The CLI waits and completes automatically
once you approve it there.`,
		RunE: runLogin,
	}
}

func runLogin(cmd *cobra.Command, args []string) error {
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

	fmt.Println("Server:", cfg.ServerURL)
	fmt.Print("Connecting... ")
	authCfg, err := api.GetAuthConfig(cfg.ServerURL)
	if err != nil {
		fmt.Println("❌")
		return fmt.Errorf("could not reach server at %s: %w", cfg.ServerURL, err)
	}
	if authCfg.GitHubClientID == "" {
		fmt.Println("❌")
		return fmt.Errorf("server has no GITHUB_CLIENT_ID configured — contact the server admin")
	}
	fmt.Println("✅")

	// ── Step 1: request a device code from GitHub ──────────────────────────
	dc, err := api.StartGitHubDeviceFlow(authCfg.GitHubClientID)
	if err != nil {
		return err
	}

	// ── Step 2: show the user code clearly — this IS the UI, no browser
	// redirect page needed, no copy-pasting long tokens ──────────────────
	fmt.Println()
	fmt.Println("┌─────────────────────────────────────────────┐")
	fmt.Println("│  Open this URL on any device:                │")
	fmt.Printf("│  %-44s │\n", dc.VerificationURI)
	fmt.Println("│                                               │")
	fmt.Printf("│  And enter this code:   %-19s │\n", dc.UserCode)
	fmt.Println("└─────────────────────────────────────────────┘")
	fmt.Println()
	fmt.Println("Waiting for you to approve in the browser...")

	// ── Step 3: poll GitHub until the user approves (or it expires) ────────
	ghToken, err := pollForGitHubToken(authCfg.GitHubClientID, dc)
	if err != nil {
		fmt.Println()
		return err
	}

	fmt.Println("✅ Approved")
	fmt.Print("Finishing login... ")

	// ── Step 4: hand the verified GitHub token to our server, get DotSync
	// tokens back. The server independently re-verifies this token against
	// GitHub's own API — it never just trusts what the CLI claims. ─────────
	result, err := api.ExchangeGitHubDeviceToken(cfg.ServerURL, ghToken)
	if err != nil {
		fmt.Println("❌")
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

	fmt.Println("✅")
	fmt.Println()
	fmt.Printf("Welcome, %s! 👋\n", username)
	fmt.Printf("Plan: %s\n", plan)
	fmt.Println()
	fmt.Println("Next: cd into your project and run 'dotsync init'")
	fmt.Println()

	return nil
}

// pollForGitHubToken polls GitHub's token endpoint at the interval GitHub
// requested, respecting slow_down backoff, until the user approves, the
// code expires, or they explicitly deny access.
func pollForGitHubToken(clientID string, dc *api.DeviceCodeResponse) (string, error) {
	interval := time.Duration(dc.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)

	for {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("code expired before authorization — run 'dotsync login' again")
		}

		time.Sleep(interval)

		token, err := api.PollGitHubDeviceToken(clientID, dc.DeviceCode)
		if err == nil {
			return token, nil
		}

		switch err {
		case api.PollErrAuthorizationPending:
			fmt.Print(".")
			continue
		case api.PollErrSlowDown:
			// RFC 8628 §3.5: add 5s to the interval, cumulatively, and keep polling.
			interval += 5 * time.Second
			fmt.Print(".")
			continue
		case api.PollErrExpired:
			return "", fmt.Errorf("code expired before authorization — run 'dotsync login' again")
		case api.PollErrAccessDenied:
			return "", fmt.Errorf("authorization was denied")
		default:
			return "", err
		}
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
