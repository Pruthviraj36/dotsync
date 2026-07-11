package cmd

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
	"github.com/Pruthviraj36/dotsync/internal/model"
)

func billingCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "billing",
		Short: "dotsync is free — buy an on-premise license if you want to self-host",
	}
	c.AddCommand(
		billingStatusCmd(),
		billingPlansCmd(),
		billingOnPremiseCmd(),
	)
	return c
}

// dotsync billing status
func billingStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show your current plan",
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

			fmt.Println()
			fmt.Println(bold("💳 DotSync Billing Status"))
			fmt.Println()
			fmt.Printf("  %-16s %s\n", bold("Plan:"), planBadge(plan))
			fmt.Println()
			fmt.Printf("  %s\n", green("✓ Every feature is included — unlimited projects, members,"))
			fmt.Printf("  %s\n", green("  history, audit logs, and leak detection. Free."))

			if plan != "onpremise" {
				fmt.Println()
				fmt.Printf("  %s\n", dim("Want to run dotsync on your own infrastructure instead of"))
				fmt.Printf("  %s\n", dim("the hosted service? → "+cyan("dotsync billing onpremise")))
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
		Short: "Show pricing",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println()
			fmt.Println(bold("📋 DotSync Pricing"))
			fmt.Println()

			fmt.Printf("  %s\n", dim(strings.Repeat("─", 60)))
			fmt.Printf("  %s  %s\n", cyan(fmt.Sprintf("%-12s", "Free")), "$0 — hosted at dotsync.onrender.com")
			fmt.Printf("      %s\n", "Every feature included: unlimited projects, members,")
			fmt.Printf("      %s\n", "history, audit logs, leak detection.")
			fmt.Println()
			fmt.Printf("  %s  %s\n", yellow(fmt.Sprintf("%-12s", "On-Premise")), fmt.Sprintf("$%d one-time — run it on your own servers", model.OnPremisePriceUSD))
			fmt.Printf("      %s\n", "Same feature set. You host it, you control it.")
			fmt.Printf("  %s\n", dim(strings.Repeat("─", 60)))

			fmt.Println()
			fmt.Printf("  %s\n", dim("Run 'dotsync billing onpremise' to buy the self-host license"))
			fmt.Println()
			return nil
		},
	}
}

// dotsync billing onpremise
func billingOnPremiseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "onpremise",
		Short: fmt.Sprintf("Buy the one-time $%d on-premise license", model.OnPremisePriceUSD),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadGlobal()
			if err != nil || !config.IsLoggedIn(cfg) {
				return fmt.Errorf("not logged in — run: dotsync login")
			}

			fmt.Println()
			fmt.Printf("  %s\n", bold(fmt.Sprintf("On-premise license — $%d one-time", model.OnPremisePriceUSD)))
			fmt.Printf("  %s\n", dim("Self-host dotsync on your own infrastructure. Same features"))
			fmt.Printf("  %s\n", dim("as the hosted version — this just removes the dependency on"))
			fmt.Printf("  %s\n", dim("dotsync.onrender.com."))
			fmt.Println()

			client := api.New(cfg)
			result, err := client.BillingCheckout("onpremise")
			if err != nil {
				return fmt.Errorf("checkout: %w", err)
			}

			checkoutURL := fmt.Sprintf("%v", result["checkout_url"])
			if checkoutURL == "" || checkoutURL == "<nil>" {
				return fmt.Errorf("no checkout URL returned — billing may not be configured on the server yet")
			}

			fmt.Printf("  %s\n", bold("Opening checkout..."))
			fmt.Println()
			fmt.Printf("  %s\n", cyan(checkoutURL))
			fmt.Println()

			_ = openBrowser(checkoutURL)
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
	case "onpremise":
		return yellow("on-premise")
	default:
		return dim("free")
	}
}
