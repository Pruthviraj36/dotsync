package cmd

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
)

func auditCmd() *cobra.Command {
	var envFlag string

	cmd := &cobra.Command{
		Use:   "audit",
		Short: "View the audit log for this project",
		Long: `Shows who pushed, pulled, and changed team membership.
Each event is recorded with user, timestamp, IP, and metadata.
Available to all users — no plan required.`,
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
				return err
			}

			if envFlag != "" {
				var filtered []map[string]any
				for _, l := range logs {
					if e, _ := l["env"].(string); e == envFlag {
						filtered = append(filtered, l)
					}
				}
				logs = filtered
			}

			title := fmt.Sprintf("Audit Log · %s", projCfg.ProjectSlug)
			if envFlag != "" {
				title += " (" + envFlag + ")"
			}
			banner(title)

			if len(logs) == 0 {
				fmt.Println(info(fmt.Sprintf("No audit events recorded for %s.", projCfg.ProjectSlug)))
				blank()
				return nil
			}

			var rows [][]string
			for _, entry := range logs {
				action, _ := entry["action"].(string)
				username, _ := entry["username"].(string)
				envName, _ := entry["env"].(string)
				createdAt, _ := entry["created_at"].(string)
				metaStr, _ := entry["metadata"].(string)
				age := ""
				if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
					age = formatAge(t)
				}
				rows = append(rows, []string{
					age,
					"@" + username,
					actionColor(action),
					envName,
					parseAuditDetail(action, metaStr),
				})
			}

			table([]string{"Time", "User", "Action", "Environment", "Detail"}, rows)
			hint(fmt.Sprintf("%d audit event(s) recorded.", len(rows)))
			blank()
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
	case "token_create", "token_revoke":
		if n, ok := meta["token_name"].(string); ok {
			return n
		}
	}
	return ""
}
