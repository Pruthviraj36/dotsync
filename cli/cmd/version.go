package cmd

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/config"
)

var Version = "dev"

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show version, build info, and active context",
		Run: func(cmd *cobra.Command, args []string) {
			version := Version
			revision := ""
			buildTime := ""
			dirty := false

			if info, ok := debug.ReadBuildInfo(); ok {
				if version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
					version = info.Main.Version
				}
				for _, s := range info.Settings {
					switch s.Key {
					case "vcs.revision":
						revision = s.Value
						if len(revision) > 7 {
							revision = revision[:7]
						}
					case "vcs.time":
						if t, err := time.Parse(time.RFC3339, s.Value); err == nil {
							buildTime = t.Format("2006-01-02")
						}
					case "vcs.modified":
						dirty = s.Value == "true"
					}
				}
			}

			cfg, _ := config.LoadGlobal()
			projCfg, _ := config.LoadProject()

			banner("DotSync System & Version")

			var rows [][]string
			rows = append(rows, []string{"Version", version, green("installed")})
			rows = append(rows, []string{"Platform", fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH), "target"})
			rows = append(rows, []string{"Go Runtime", strings.TrimPrefix(runtime.Version(), "go"), "toolchain"})

			if revision != "" {
				revDisplay := revision
				if dirty {
					revDisplay += " (modified)"
				}
				rows = append(rows, []string{"Git Commit", revDisplay, "vcs"})
			}
			if buildTime != "" {
				rows = append(rows, []string{"Build Date", buildTime, "timestamp"})
			}

			// Server
			if cfg != nil && cfg.ServerURL != "" {
				rows = append(rows, []string{"Server Endpoint", cfg.ServerURL, green("configured")})
			} else {
				rows = append(rows, []string{"Server Endpoint", "not configured", yellow("missing")})
			}

			// Account
			if cfg != nil && cfg.Username != "" {
				rows = append(rows, []string{"Account", "@" + cfg.Username, green("logged in")})
			} else {
				rows = append(rows, []string{"Account", "not logged in", dim("unauthenticated")})
			}

			// Project
			if projCfg != nil {
				rows = append(rows, []string{"Active Project", fmt.Sprintf("%s (%s)", projCfg.ProjectSlug, projCfg.DefaultEnv), green("linked")})
			} else {
				rows = append(rows, []string{"Active Project", "none", dim("unlinked")})
			}

			table([]string{"Property", "Value", "Status"}, rows)
			hint("Check for updates: dotsync update")
			blank()
		},
	}
}
