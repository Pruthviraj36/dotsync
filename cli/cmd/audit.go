package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
)

func auditCmd() *cobra.Command {
	var envFlag string

	cmd := &cobra.Command{
		Use:   "audit",
		Short: "View the audit log for this project (Business plan)",
		Long: `Shows who pushed, pulled, and changed team membership in this project.
Each action is recorded server-side with the user, timestamp, IP address,
and relevant metadata.

Available on the Business plan. Shows the last 50 events.`,
		Example: `  dotsync audit
  dotsync audit --env production`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireLogin()
			if err != nil {
				return err
			}

			projCfg, err := config.LoadProject()
			if err != nil {
				return err
			}

			client := api.New(cfg)
			logs, err := client.AuditLogs(projCfg.ProjectSlug)
			if err != nil {
				if strings.Contains(err.Error(), "Business plan") ||
					strings.Contains(err.Error(), "402") {
					fmt.Println()
					fmt.Println(yellow("🔒 Audit logs require the Business plan."))
					fmt.Println("   Upgrade at: https://dotsync.onrender.com/pricing")
					fmt.Println()
					return nil
				}
				return err
			}

			if len(logs) == 0 {
				fmt.Printf("\n📋 No audit events yet for '%s'\n\n", projCfg.ProjectSlug)
				return nil
			}

			// Filter by env if specified
			if envFlag != "" {
				var filtered []map[string]any
				for _, log := range logs {
					if env, ok := log["env"].(string); ok && env == envFlag {
						filtered = append(filtered, log)
					}
				}
				logs = filtered
			}

			fmt.Printf("\n"+bold("📋 Audit Log — %s"), projCfg.ProjectSlug)
			if envFlag != "" {
				fmt.Printf(" (%s)", envFlag)
			}
			fmt.Println()
			fmt.Println(strings.Repeat("─", 65))
			fmt.Printf("  "+bold("%-18s")+" "+bold("%-10s")+" "+bold("%-12s")+" "+bold("%-10s")+" "+bold("%s")+"\n",
				"WHEN", "WHO", "ACTION", "ENV", "DETAIL")
			fmt.Println(strings.Repeat("─", 65))

			for _, entry := range logs {
				action, _ := entry["action"].(string)
				username, _ := entry["username"].(string)
				envName, _ := entry["env"].(string)
				createdAtStr, _ := entry["created_at"].(string)
				metaStr, _ := entry["metadata"].(string)

				age := ""
				if createdAtStr != "" {
					if t, err := time.Parse(time.RFC3339, createdAtStr); err == nil {
						age = formatAge(t)
					}
				}

				detail := parseAuditDetail(action, metaStr)
				icon := actionIcon(action)

				fmt.Printf("  "+dim("%-18s")+" "+cyan("%-10s")+" %s "+yellow("%-8s")+" "+blue("%-12s")+" %s\n",
				age, "@"+username, icon, action, envName, detail)
			}

			fmt.Println(strings.Repeat("─", 65))
			fmt.Printf("  %d event(s) shown\n\n", len(logs))
			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "filter by environment")
	return cmd
}

func actionIcon(action string) string {
	icons := map[string]string{
		"push":   "📤",
		"pull":   "📥",
		"invite": "👤",
		"revoke": "🚫",
		"login":  "🔑",
		"logout": "🚪",
	}
	if icon, ok := icons[action]; ok {
		return icon
	}
	return "📝"
}

func parseAuditDetail(action, metaJSON string) string {
	if metaJSON == "" {
		return ""
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
		return ""
	}

	switch action {
	case "push", "pull":
		if v, ok := meta["version"]; ok {
			return fmt.Sprintf("v%.0f", v)
		}
	case "invite":
		if u, ok := meta["invited_user"].(string); ok {
			return "@" + u
		}
	case "revoke":
		if u, ok := meta["removed_user"].(string); ok {
			return "@" + u
		}
	}
	return ""
}

// UpdateTeamMemberAuditMeta is called by team handlers to enrich audit logs
// with the target user — not exported, used internally.
func getAuditMeta(key, value string) map[string]any {
	return map[string]any{key: value}
}
