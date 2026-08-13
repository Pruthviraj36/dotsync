package cmd

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

var Version = "dev"

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version number of DotSync",
		Run: func(cmd *cobra.Command, args []string) {
			version := Version
			revision := "unknown"
			buildTime := "unknown"

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
						buildTime = s.Value
					}
				}
			}

			blank()
			fmt.Printf("  %s\n", bold("DotSync"))
			blank()
			kv("Version  ", version)
			kv("Revision ", dim(revision))
			kv("Built    ", dim(buildTime))
			kv("Platform ", dim(fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)))
			kv("Go       ", dim(runtime.Version()))
			blank()
		},
	}
}
