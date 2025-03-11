package cmd

import (
	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/pkg/logger"
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
			logger.Info(info.String())

			hasUpdate, latestVersion, err := common.CheckForNewVersion(
				info.Version,
				info.ProxyURL,
			)
			if err != nil {
				logger.Error("Error checking for updates", "error", err)
				return
			}

			if hasUpdate {
				logger.Info("A new version is available", "version", latestVersion)
				logger.Info("You can update using: gordon update")
			}
		},
	}
	return cmd
}
