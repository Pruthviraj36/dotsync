package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
)

func billingCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "billing",
		Short: "Manage your DotSync subscription",
	}
	c.AddCommand(
		billingStatusCmd(),
		billingPlansCmd(),
		billingUpgradeCmd(),
		billingManageCmd(),
	)
	return c
}

// dotsync billing status
func billingStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show your current plan and limits",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadGlobal()
			if err != nil || !config.IsLoggedIn(cfg) {
				return fmt.Errorf("not logged in — run: dotsync login")
			}
			client := api.New(cfg)
			status, err := client.BillingStatus()
			if err != nil {
				return fmt.Errorf("billing status: %w", err)
			}

			plan := fmt.Sprintf("%v", status["plan"])
			limits, _ := status["limits"].(map[string]any)

			fmt.Println()
			fmt.Println(bold("💳 DotSync Billing Status"))
			fmt.Println()
			fmt.Printf("  %-16s %s\n", bold("Plan:"), planBadge(plan))

			if hasSub, _ := status["has_subscription"].(bool); hasSub {
				fmt.Printf("  %-16s %s\n", bold("Subscription:"), green("Active"))
			} else if plan == "free" {
				fmt.Printf("  %-16s %s\n", bold("Subscription:"), dim("Free tier"))
			}

			if limits != nil {
				fmt.Println()
				fmt.Println(bold("  Limits:"))

				projects := limits["max_projects"]
				if fmt.Sprintf("%v", projects) == "-1" {
					fmt.Printf("    %-18s %s\n", "Projects:", green("Unlimited"))
				} else {
					fmt.Printf("    %-18s %v\n", "Projects:", projects)
				}

				members := limits["max_members"]
				if fmt.Sprintf("%v", members) == "-1" {
					fmt.Printf("    %-18s %s\n", "Team members:", green("Unlimited"))
				} else {
					fmt.Printf("    %-18s %v\n", "Team members:", members)
				}

				history := limits["history_days"]
				fmt.Printf("    %-18s %v days\n", "History:", history)

				if al, _ := limits["audit_logs"].(bool); al {
					fmt.Printf("    %-18s %s\n", "Audit logs:", green("✅ Included"))
				} else {
					fmt.Printf("    %-18s %s\n", "Audit logs:", dim("❌ Business plan"))
				}

				if ld, _ := limits["leak_detect"].(bool); ld {
					fmt.Printf("    %-18s %s\n", "Leak detection:", green("✅ Included"))
				} else {
					fmt.Printf("    %-18s %s\n", "Leak detection:", dim("❌ Pro+ plan"))
				}
			}

			if plan == "free" {
				fmt.Println()
				fmt.Printf("  %s\n", yellow("→  Upgrade anytime: "+cyan("dotsync billing upgrade")))
			} else {
				fmt.Println()
				fmt.Printf("  %s\n", dim("Manage subscription: dotsync billing manage"))
			}
			fmt.Println()
			return nil
		},
	}
}

// dotsync billing plans
func billingPlansCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "plans",
		Short: "Show all available plans and pricing",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadGlobal()
			if err != nil {
				cfg = &config.GlobalConfig{ServerURL: "https://dotsync.onrender.com"}
			}
			client := api.New(cfg)
			plansResp, err := client.BillingPlans()
			if err != nil {
				return fmt.Errorf("fetch plans: %w", err)
			}

			plans, _ := plansResp["plans"].([]any)

			fmt.Println()
			fmt.Println(bold("📋 DotSync Plans"))
			fmt.Println()

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
				bold("PLAN"), bold("PRICE"), bold("PROJECTS"), bold("MEMBERS"), bold("HISTORY"), bold("AUDIT"))
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
				strings.Repeat("─", 8), strings.Repeat("─", 10), strings.Repeat("─", 10),
				strings.Repeat("─", 9), strings.Repeat("─", 10), strings.Repeat("─", 7))

			for _, p := range plans {
				plan := p.(map[string]any)
				name := fmt.Sprintf("%v", plan["name"])
				price := fmt.Sprintf("$%v/mo", plan["price_usd"])
				if plan["price_usd"] == float64(0) {
					price = "Free"
				}

				projects := fmt.Sprintf("%v", plan["max_projects"])
				if projects == "-1" {
					projects = "Unlimited"
				}

				members := fmt.Sprintf("%v", plan["max_members"])
				if members == "-1" {
					members = "Unlimited"
				}

				history := fmt.Sprintf("%v days", plan["history_days"])

				auditLogs := "❌"
				if al, _ := plan["audit_logs"].(bool); al {
					auditLogs = "✅"
				}

				fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
					planBadge(strings.ToLower(name)), price, projects, members, history, auditLogs)
			}
			w.Flush()

			fmt.Println()
			fmt.Printf("  %s\n", dim("Run 'dotsync billing upgrade' to subscribe"))
			fmt.Println()
			return nil
		},
	}
}

// dotsync billing upgrade [plan]
func billingUpgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade [plan]",
		Short: "Upgrade your plan (pro, team, business)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadGlobal()
			if err != nil || !config.IsLoggedIn(cfg) {
				return fmt.Errorf("not logged in — run: dotsync login")
			}

			plan := ""
			if len(args) > 0 {
				plan = strings.ToLower(args[0])
			}

			// Prompt if not provided
			if plan == "" {
				fmt.Println()
				fmt.Println(bold("📦 Choose a plan to upgrade to:"))
				fmt.Println()
				fmt.Printf("  %s  pro      → $9/mo  — Unlimited projects, 5 members, 30-day history\n", cyan("1."))
				fmt.Printf("  %s  team     → $29/mo — Unlimited projects, 10 members, 90-day history\n", cyan("2."))
				fmt.Printf("  %s  business → $79/mo — Unlimited everything, audit logs\n", cyan("3."))
				fmt.Println()
				fmt.Print("  Enter plan name or number: ")

				var input string
				fmt.Scanln(&input)
				input = strings.TrimSpace(strings.ToLower(input))

				switch input {
				case "1", "pro":
					plan = "pro"
				case "2", "team":
					plan = "team"
				case "3", "business":
					plan = "business"
				default:
					return fmt.Errorf("invalid plan %q — choose: pro, team, business", input)
				}
			}

			valid := map[string]bool{"pro": true, "team": true, "business": true}
			if !valid[plan] {
				return fmt.Errorf("invalid plan %q — choose: pro, team, business", plan)
			}

			fmt.Printf("\n%s\n", dim("⏳ Creating checkout session for "+plan+" plan..."))

			client := api.New(cfg)
			resp, err := client.BillingCheckout(plan)
			if err != nil {
				return fmt.Errorf("checkout: %w", err)
			}

			checkoutURL, _ := resp["checkout_url"].(string)
			if checkoutURL == "" {
				return fmt.Errorf("no checkout URL returned from server")
			}

			fmt.Println()
			fmt.Printf("  %s\n", green("✅ Checkout session created!"))
			fmt.Println()
			fmt.Printf("  %s\n", bold("Opening your browser to complete payment..."))
			fmt.Printf("  %s\n", dim("If the browser doesn't open, visit this URL manually:"))
			fmt.Println()
			fmt.Printf("  %s\n", cyan(checkoutURL))
			fmt.Println()

			if err := openBrowser(checkoutURL); err != nil {
				fmt.Printf("  %s\n", yellow("⚠️  Could not open browser automatically."))
				fmt.Printf("  %s\n", dim("Copy the URL above and paste it in your browser."))
			}

			fmt.Printf("  %s\n", dim("After payment, your plan updates automatically within seconds."))
			fmt.Printf("  %s\n", dim("Run 'dotsync billing status' to confirm."))
			fmt.Println()
			return nil
		},
	}
}

// dotsync billing manage  — opens Stripe Customer Portal
func billingManageCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "manage",
		Short: "Manage subscription, payment method, or cancel",
		Long: `Opens the Stripe Customer Portal in your browser.

From there you can:
  • Cancel your subscription
  • Update your payment method
  • Download past invoices
  • Switch plans`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadGlobal()
			if err != nil || !config.IsLoggedIn(cfg) {
				return fmt.Errorf("not logged in — run: dotsync login")
			}

			fmt.Printf("\n%s\n", dim("⏳ Opening billing portal..."))

			client := api.New(cfg)
			resp, err := client.BillingPortal()
			if err != nil {
				if strings.Contains(err.Error(), "no billing account") {
					return fmt.Errorf("no active subscription found — run: dotsync billing upgrade")
				}
				return fmt.Errorf("billing portal: %w", err)
			}

			portalURL, _ := resp["portal_url"].(string)
			if portalURL == "" {
				return fmt.Errorf("no portal URL returned from server")
			}

			fmt.Println()
			fmt.Printf("  %s\n", green("✅ Portal session created!"))
			fmt.Printf("  %s\n", bold("Opening Stripe billing portal in your browser..."))
			fmt.Println()
			fmt.Printf("  %s\n", cyan(portalURL))
			fmt.Println()

			if err := openBrowser(portalURL); err != nil {
				fmt.Printf("  %s\n", yellow("⚠️  Could not open browser automatically."))
			}
			fmt.Println()
			return nil
		},
	}
}

// openBrowser opens a URL in the default system browser cross-platform.
func openBrowser(url string) error {
	var browserCmd string
	var args []string
	switch runtime.GOOS {
	case "windows":
		browserCmd = "cmd"
		args = []string{"/c", "start", url}
	case "darwin":
		browserCmd = "open"
		args = []string{url}
	default: // linux, bsd, etc
		browserCmd = "xdg-open"
		args = []string{url}
	}
	return exec.Command(browserCmd, args...).Start()
}

// planBadge returns a colored plan name string.
func planBadge(plan string) string {
	switch plan {
	case "free":
		return dim("free")
	case "pro":
		return cyan("pro")
	case "team":
		return blue("team")
	case "business":
		return yellow("business ⭐")
	default:
		return plan
	}
}
