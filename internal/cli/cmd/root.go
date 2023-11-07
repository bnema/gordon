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
	cmd := &cobra.Command{
		Use:   "gordon",
		Short: "Gordon is a CLI for the Gordon project",
	}

	backendVersion, err := getBackendVersion(a)
	if err != nil {
		fmt.Println("Error getting backend version:", err)
		return cmd
	}

	// Using color.Green function to print in green
	color.Green("Gordon %s", a.Config.GetVersion())
	color.Blue("Gordon Backend %s", backendVersion)

	return cmd
}

func getBackendVersion(a *cli.App) (string, error) {
	pingResp, err := handler.PerformPingRequest(a)
	if err != nil {
		return "", err
	}

	return pingResp.Version, nil
}
