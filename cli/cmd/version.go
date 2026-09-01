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

			w := termWidth()
			rule := dim(strings.Repeat("─", w-2))

			blank()

			// ── Title bar ─────────────────────────────────────────────────────
			fmt.Printf("  %s  %s",
				boldCyan("dotsync"),
				bold(version),
			)
			if revision != "" {
				rev := revision
				if dirty {
					rev += dim("+dirty")
				}
				fmt.Printf("  %s", dim(rev))
			}
			if buildTime != "" {
				fmt.Printf("  %s", dim(buildTime))
			}
			fmt.Println()

			// ── Horizontal rule ───────────────────────────────────────────────
			fmt.Printf("  %s\n", rule)
			blank()

			// ── Build section ─────────────────────────────────────────────────
			fmt.Printf("  %s\n", dim("build"))
			fmt.Printf("  %-10s  %s\n", dim("platform"), bold(fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)))
			fmt.Printf("  %-10s  %s\n", dim("go"), dim(strings.TrimPrefix(runtime.Version(), "go")))
			if revision != "" {
				revDisplay := revision
				if dirty {
					revDisplay += "  " + yellow("(modified)")
				}
				fmt.Printf("  %-10s  %s\n", dim("commit"), dim(revDisplay))
			}

			blank()

			// ── Context section ───────────────────────────────────────────────
			fmt.Printf("  %s\n", dim("context"))

			// Server
			if cfg != nil && cfg.ServerURL != "" {
				fmt.Printf("  %-10s  %s\n", dim("server"), cyan(cfg.ServerURL))
			} else {
				fmt.Printf("  %-10s  %s  %s\n", dim("server"),
					red("not configured"),
					dim("→ dotsync config set-server <url>"),
				)
			}

			// Account
			if cfg != nil && cfg.Username != "" {
				fmt.Printf("  %-10s  %s\n", dim("logged in"), boldCyan("@"+cfg.Username))
			} else {
				fmt.Printf("  %-10s  %s  %s\n", dim("logged in"),
					red("no"),
					dim("→ dotsync login"),
				)
			}

			// Project
			if projCfg != nil {
				fmt.Printf("  %-10s  %s  %s\n",
					dim("project"),
					bold(projCfg.ProjectSlug),
					dim(projCfg.DefaultEnv),
				)
			} else {
				fmt.Printf("  %-10s  %s  %s\n", dim("project"),
					dim("none"),
					dim("→ dotsync init"),
				)
			}

			blank()
			fmt.Printf("  %s\n", rule)
			blank()
		},
	}
}
