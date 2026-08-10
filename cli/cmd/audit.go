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
Available to all users.`,
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
					if e, ok := l["env"].(string); ok && e == envFlag {
						filtered = append(filtered, l)
					}
				}
				logs = filtered
			}

			if len(logs) == 0 {
				blank()
				fmt.Println(info(fmt.Sprintf("No audit events yet for %s.", projCfg.ProjectSlug)))
				blank()
				return nil
			}

			type row struct{ when, who, action, env, detail string }
			rows := make([]row, 0, len(logs))
			for _, entry := range logs {
				action, _     := entry["action"].(string)
				username, _   := entry["username"].(string)
				envName, _    := entry["env"].(string)
				createdAt, _  := entry["created_at"].(string)
				metaStr, _    := entry["metadata"].(string)

				age := ""
				if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
					age = formatAge(t)
				}

				rows = append(rows, row{
					when:   age,
					who:    "@" + username,
					action: action,
					env:    envName,
					detail: parseAuditDetail(action, metaStr),
				})
			}

			// Dynamic column widths
			whenW, whoW, actionW, envW := 4, 3, 6, 3
			for _, r := range rows {
				if w := len(r.when);   w > whenW   { whenW = w }
				if w := len(r.who);    w > whoW     { whoW = w }
				if w := len(r.action); w > actionW  { actionW = w }
				if w := len(r.env);    w > envW     { envW = w }
			}

			rw := whenW + whoW + actionW + envW + 16
			rl := ruleN(rw)

			title := fmt.Sprintf("Audit Log — %s", projCfg.ProjectSlug)
			if envFlag != "" {
				title += " (" + envFlag + ")"
			}

			blank()
			fmt.Printf("  %s\n", bold(title))
			blank()
			tableHeader(rl,
				[]string{"WHEN", "WHO", "ACTION", "ENV", "DETAIL"},
				[]int{whenW, whoW, actionW, envW},
			)

			for _, r := range rows {
				fmt.Printf("  %-*s  %-*s  %-*s  %-*s  %s\n",
					whenW, dim(r.when),
					whoW, cyan(r.who),
					actionW, actionColor(r.action),
					envW, blue(r.env),
					dim(r.detail),
				)
			}

			fmt.Println("  " + rl)
			fmt.Printf("  %s\n", dim(fmt.Sprintf("%d event(s)", len(rows))))
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

func getAuditMeta(key, value string) map[string]any {
	return map[string]any{key: value}
}
