package cmd

import (
	"fmt"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/common"
	"github.com/spf13/cobra"
)

func NewVersionCommand(app *cli.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			info := common.GetVersionInfo(
				app.Config.Build.BuildVersion,
				app.Config.Build.BuildCommit,
				app.Config.Build.BuildDate,
				app.Config.Build.ProxyURL,
			)
			fmt.Println(info.String())

			hasUpdate, latestVersion, err := common.CheckForNewVersion(
				info.Version,
				info.ProxyURL,
			)
			if err != nil {
				fmt.Printf("Error checking for updates: %v\n", err)
				return
			}

			if hasUpdate {
				fmt.Printf("\nA new version is available: %s\n", latestVersion)
				fmt.Println("You can update using: gordon update")
			}
		},
	}
	return cmd
}
