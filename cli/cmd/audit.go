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
					fmt.Println(yellow("Audit logs require the Business plan."))
					fmt.Println("Upgrade at: https://dotsync.onrender.com/pricing")
					fmt.Println()
					return nil
				}
				return err
			}

			if len(logs) == 0 {
				fmt.Printf("\nNo audit events yet for '%s'\n\n", projCfg.ProjectSlug)
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

			type row struct {
				when, who, action, env, detail string
			}

			rows := make([]row, 0, len(logs))
			for _, entry := range logs {
				action, _ := entry["action"].(string)
				username, _ := entry["username"].(string)
				envName, _ := entry["env"].(string)
				createdAtStr, _ := entry["created_at"].(string)
				metaStr, _ := entry["metadata"].(string)

				when := ""
				if createdAtStr != "" {
					if t, err := time.Parse(time.RFC3339, createdAtStr); err == nil {
						when = formatAge(t)
					}
				}

				rows = append(rows, row{
					when:   when,
					who:    "@" + username,
					action: action,
					env:    envName,
					detail: parseAuditDetail(action, metaStr),
				})
			}

			// Size each column to fit its longest value (including the
			// header) rather than a fixed guess, so nothing gets truncated
			// or drifts out of alignment with wider-than-expected data.
			whenW, whoW, actionW, envW := len("WHEN"), len("WHO"), len("ACTION"), len("ENV")
			for _, r := range rows {
				whenW = max(whenW, len(r.when))
				whoW = max(whoW, len(r.who))
				actionW = max(actionW, len(r.action))
				envW = max(envW, len(r.env))
			}

			title := fmt.Sprintf("Audit Log — %s", projCfg.ProjectSlug)
			if envFlag != "" {
				title += fmt.Sprintf(" (%s)", envFlag)
			}
			ruleWidth := whenW + whoW + actionW + envW + 20 // + spacing between columns
			rule := strings.Repeat("─", ruleWidth)

			fmt.Println()
			fmt.Println(bold(title))
			fmt.Println(rule)
			fmt.Printf("  "+bold("%-*s")+"  "+bold("%-*s")+"  "+bold("%-*s")+"  "+bold("%-*s")+"  "+bold("%s")+"\n",
				whenW, "WHEN", whoW, "WHO", actionW, "ACTION", envW, "ENV", "DETAIL")
			fmt.Println(rule)

			for _, r := range rows {
				fmt.Printf("  "+dim("%-*s")+"  "+cyan("%-*s")+"  "+yellow("%-*s")+"  "+blue("%-*s")+"  %s\n",
					whenW, r.when, whoW, r.who, actionW, r.action, envW, r.env, r.detail)
			}

			fmt.Println(rule)
			fmt.Printf("  %d event(s) shown\n\n", len(rows))
			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "filter by environment")
	return cmd
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
