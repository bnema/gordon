// the root command is the entrypoint for the gordon cli (default: client)
package cmd

import (
	"fmt"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/cli/handler"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// NewRootCommand creates a new root command
func NewRootCommand(a *cli.App) *cobra.Command {
	return &cobra.Command{
		Run: func(cmd *cobra.Command, args []string) {

			backendVersion := getBackendVersion(a)
			color.Green("Gordon %s", a.Config.GetVersion())
			color.Blue("Gordon Backend %s", backendVersion)

			if backendVersion != a.Config.GetVersion() {
				color.Yellow("A new version of Gordon is available")
			}

			fmt.Println()
			fmt.Println("Use \"gordon --help\" for more information about a command.")
		},
	}
}

func getBackendVersion(a *cli.App) string {
	pingResp, err := handler.PerformPingRequest(a)
	if err != nil {
		return fmt.Sprintf("Error: %s", err)
	}

	return pingResp.Version
}
