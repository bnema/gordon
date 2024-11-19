package cmd

import (
	"fmt"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/common"
	"github.com/spf13/cobra"
)

func NewVersionCommand(a *cli.App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Display the version of Gordon",
		Run: func(cmd *cobra.Command, args []string) {
			info := common.GetVersionInfo(
				a.Config.Build.BuildVersion,
				a.Config.Build.BuildCommit,
				a.Config.Build.BuildDate,
			)
			fmt.Println(info.String())

			// Check for updates
			hasUpdate, latestVersion, err := common.CheckForNewVersion(info.Version)
			if err != nil {
				fmt.Printf("Error checking for updates: %v\n", err)
				return
			}

			if hasUpdate {
				fmt.Printf("\nNew version %s available! Visit: https://github.com/bnema/gordon/releases/latest\n", latestVersion)
			}
		},
	}
}
