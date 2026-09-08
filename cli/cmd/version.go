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

			sectionTitle("dotsync " + version)
			if revision != "" {
				rev := revision
				if dirty {
					rev += dim("+dirty")
				}
				kvDim("commit", rev)
			}
			if buildTime != "" {
				kvDim("built", buildTime)
			}

			sectionTitle("Build")
			kv("platform", bold(fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)))
			kvDim("go", strings.TrimPrefix(runtime.Version(), "go"))
			if revision != "" {
				revDisplay := revision
				if dirty {
					revDisplay += "  " + yellow("(modified)")
				}
				kvDim("commit", revDisplay)
			}

			sectionTitle("Context")

			// Server
			if cfg != nil && cfg.ServerURL != "" {
				kvCyan("server", cfg.ServerURL)
			} else {
				kv("server", red("not configured")+dim(" → dotsync config set-server <url>"))
			}

			// Account
			if cfg != nil && cfg.Username != "" {
				kvCyan("account", "@"+cfg.Username)
			} else {
				kv("account", red("not logged in")+dim(" → dotsync login"))
			}

			// Project
			if projCfg != nil {
				kv("project", bold(projCfg.ProjectSlug)+dim(" · "+projCfg.DefaultEnv))
			} else {
				kv("project", dim("none → dotsync init"))
			}
			blank()
		},
	}
}
