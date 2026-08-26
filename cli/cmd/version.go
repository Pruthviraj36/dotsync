package cmd

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/config"
)

var Version = "dev"

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and environment info",
		Run: func(cmd *cobra.Command, args []string) {
			version := Version
			revision := "unknown"
			buildTime := "unknown"
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
							buildTime = t.Format("2006-01-02 15:04 UTC")
						}
					case "vcs.modified":
						dirty = s.Value == "true"
					}
				}
			}

			if dirty {
				revision += " (modified)"
			}

			cfg, _ := config.LoadGlobal()
			projCfg, _ := config.LoadProject()

			blank()
			fmt.Printf("  %s  %s\n", bold("DotSync"), cyan(version))
			blank()

			// Build info
			kvDim("Commit", revision)
			kvDim("Built", buildTime)
			kvDim("Platform", fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH))
			kvDim("Go", runtime.Version())
			blank()

			// Runtime context
			if cfg != nil && cfg.ServerURL != "" {
				kvCyan("Server", cfg.ServerURL)
			} else {
				kvRed("Server", "not configured")
			}
			if cfg != nil && cfg.Username != "" {
				kvCyan("Account", "@"+cfg.Username)
			} else {
				kv("Account", dim("not logged in"))
			}
			if projCfg != nil {
				kvCyan("Project", projCfg.ProjectSlug+"/"+projCfg.DefaultEnv)
			} else {
				kv("Project", dim("not linked  (run dotsync init)"))
			}
			blank()
		},
	}
}
