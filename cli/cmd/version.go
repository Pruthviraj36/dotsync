package cmd

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

var Version = "dev" // Can be overridden by ldflags

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
				for _, setting := range info.Settings {
					if setting.Key == "vcs.revision" {
						revision = setting.Value
						if len(revision) > 7 {
							revision = revision[:7]
						}
					}
					if setting.Key == "vcs.time" {
						buildTime = setting.Value
					}
				}
			}

			fmt.Println("DotSync CLI")
			fmt.Printf("  Version:    %s\n", version)
			fmt.Printf("  Revision:   %s\n", revision)
			fmt.Printf("  Build Time: %s\n", buildTime)
			fmt.Printf("  OS/Arch:    %s/%s\n", runtime.GOOS, runtime.GOARCH)
			fmt.Printf("  Go Version: %s\n", runtime.Version())
		},
	}
}
