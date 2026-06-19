package cmd

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version number of DotSync",
		Run: func(cmd *cobra.Command, args []string) {
			version := "unknown"
			if info, ok := debug.ReadBuildInfo(); ok {
				for _, setting := range info.Settings {
					if setting.Key == "vcs.revision" {
						// take first 7 chars of commit hash
						if len(setting.Value) > 7 {
							version = setting.Value[:7]
						} else {
							version = setting.Value
						}
					}
				}
			}
			fmt.Printf("DotSync version: %s\n", version)
		},
	}
}
