package cmd

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
)

// Payment links — create these for free on:
//   https://app.lemonsqueezy.com  (signup free, works in India, instant)
// Or:
//   https://gumroad.com           (signup free, global, no approval needed)
//
// Just create a product → set recurring price → copy the checkout link.
// No API keys, no webhooks, no SDK. Update these constants after you create them.
const (
	payLinkPro      = "https://dotsync.lemonsqueezy.com/buy/pro"
	payLinkTeam     = "https://dotsync.lemonsqueezy.com/buy/team"
	payLinkBusiness = "https://dotsync.lemonsqueezy.com/buy/business"
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

			if limits != nil {
				fmt.Println()
				fmt.Println(bold("  Limits:"))

				projects := fmt.Sprintf("%v", limits["max_projects"])
				if projects == "-1" {
					fmt.Printf("    %-18s %s\n", "Projects:", green("Unlimited"))
				} else {
					fmt.Printf("    %-18s %s\n", "Projects:", projects)
				}

				members := fmt.Sprintf("%v", limits["max_members"])
				if members == "-1" {
					fmt.Printf("    %-18s %s\n", "Team members:", green("Unlimited"))
				} else {
					fmt.Printf("    %-18s %s\n", "Team members:", members)
				}

				fmt.Printf("    %-18s %v days\n", "History:", limits["history_days"])

				if al, _ := limits["audit_logs"].(bool); al {
					fmt.Printf("    %-18s %s\n", "Audit logs:", green("included"))
				} else {
					fmt.Printf("    %-18s %s\n", "Audit logs:", dim("business plan only"))
				}

				if ld, _ := limits["leak_detect"].(bool); ld {
					fmt.Printf("    %-18s %s\n", "Leak detection:", green("included"))
				} else {
					fmt.Printf("    %-18s %s\n", "Leak detection:", dim("pro+ only"))
				}
			}

			if plan == "free" {
				fmt.Println()
				fmt.Printf("  %s\n", yellow("→  Upgrade: "+cyan("dotsync billing upgrade")))
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
			fmt.Println()
			fmt.Println(bold("📋 DotSync Plans"))
			fmt.Println()

			fmt.Printf("  %s %s %s %s %s %s\n",
				bold(fmt.Sprintf("%-10s", "PLAN")),
				bold(fmt.Sprintf("%-10s", "PRICE")),
				bold(fmt.Sprintf("%-12s", "PROJECTS")),
				bold(fmt.Sprintf("%-11s", "MEMBERS")),
				bold(fmt.Sprintf("%-12s", "HISTORY")),
				bold("AUDIT"))
			fmt.Printf("  %-10s %-10s %-12s %-11s %-12s %s\n",
				strings.Repeat("-", 8), strings.Repeat("-", 8),
				strings.Repeat("-", 9), strings.Repeat("-", 9),
				strings.Repeat("-", 9), strings.Repeat("-", 5))

			rows := []struct {
				name, price, projects, members, history string
				audit                                   bool
			}{
				{"free", "Free", "1", "3", "7 days", false},
				{"pro", "$9/mo", "Unlimited", "5", "30 days", false},
				{"team", "$29/mo", "Unlimited", "10", "90 days", false},
				{"business", "$79/mo", "Unlimited", "Unlimited", "365 days", true},
			}

			for _, row := range rows {
				audit := red("x")
				if row.audit {
					audit = green("v")
				}
				paddedName     := fmt.Sprintf("%-10s", row.name)
				paddedPrice    := fmt.Sprintf("%-10s", row.price)
				paddedProjects := fmt.Sprintf("%-12s", row.projects)
				paddedMembers  := fmt.Sprintf("%-11s", row.members)
				paddedHistory  := fmt.Sprintf("%-12s", row.history)

				var coloredName string
				switch row.name {
				case "free":
					coloredName = dim(paddedName)
				case "pro":
					coloredName = cyan(paddedName)
				case "team":
					coloredName = blue(paddedName)
				case "business":
					coloredName = yellow(paddedName)
				}
				fmt.Printf("  %s %s %s %s %s %s\n",
					coloredName, paddedPrice, paddedProjects, paddedMembers, paddedHistory, audit)
			}

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
		Use:     "upgrade [plan]",
		Short:   "Upgrade your plan (pro, team, business)",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadGlobal()
			if err != nil || !config.IsLoggedIn(cfg) {
				return fmt.Errorf("not logged in — run: dotsync login")
			}

			plan := ""
			if len(args) > 0 {
				plan = strings.ToLower(args[0])
			}

			if plan == "" {
				fmt.Println()
				fmt.Println(bold("📦 Choose a plan:"))
				fmt.Println()
				fmt.Printf("  %s  pro      → $9/mo   Unlimited projects, 5 members, 30-day history\n", cyan("1."))
				fmt.Printf("  %s  team     → $29/mo  Unlimited projects, 10 members, 90-day history\n", cyan("2."))
				fmt.Printf("  %s  business → $79/mo  Unlimited everything + audit logs\n", cyan("3."))
				fmt.Println()
				fmt.Print("  Enter plan name or number: ")

				var input string
				fmt.Scanln(&input)
				switch strings.TrimSpace(strings.ToLower(input)) {
				case "1", "pro":
					plan = "pro"
				case "2", "team":
					plan = "team"
				case "3", "business":
					plan = "business"
				default:
					return fmt.Errorf("invalid plan — choose: pro, team, business")
				}
			}

			payLink := map[string]string{
				"pro":      payLinkPro,
				"team":     payLinkTeam,
				"business": payLinkBusiness,
			}[plan]

			if payLink == "" {
				return fmt.Errorf("invalid plan %q — choose: pro, team, business", plan)
			}

			fmt.Println()
			fmt.Printf("  %s\n", bold("Opening payment page for "+planBadge(plan)+" plan..."))
			fmt.Println()
			fmt.Printf("  %s\n", cyan(payLink))
			fmt.Println()
			fmt.Printf("  %s\n", dim("After payment, email your receipt to support@dotsync.dev"))
			fmt.Printf("  %s\n", dim("Your plan will be upgraded within 24h."))
			fmt.Println()

			_ = openBrowser(payLink)
			return nil
		},
	}
}

// dotsync billing manage
func billingManageCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "manage",
		Short: "Manage or cancel your subscription",
		RunE: func(cmd *cobra.Command, args []string) error {
			manageURL := "https://app.lemonsqueezy.com/my-orders"
			fmt.Println()
			fmt.Printf("  %s\n", bold("Opening subscription management..."))
			fmt.Println()
			fmt.Printf("  %s\n", cyan(manageURL))
			fmt.Println()
			fmt.Printf("  %s\n", dim("Log in with the email you used to subscribe."))
			fmt.Println()
			_ = openBrowser(manageURL)
			return nil
		},
	}
}

func openBrowser(url string) error {
	var c string
	var a []string
	switch runtime.GOOS {
	case "windows":
		c, a = "cmd", []string{"/c", "start", url}
	case "darwin":
		c, a = "open", []string{url}
	default:
		c, a = "xdg-open", []string{url}
	}
	return exec.Command(c, a...).Start()
}

func planBadge(plan string) string {
	switch plan {
	case "free":
		return dim("free")
	case "pro":
		return cyan("pro")
	case "team":
		return blue("team")
	case "business":
		return yellow("business")
	default:
		return plan
	}
}
