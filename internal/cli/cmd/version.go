package cmd

import (
	"fmt"

	"github.com/bnema/gordon/internal/cli"
	"github.com/spf13/cobra"
)

func NewVersionCommand(a *cli.App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Display the version of Gordon",
		Run: func(cmd *cobra.Command, args []string) {
			version := a.Config.Build.BuildVersion
			if version == "" {
				fmt.Println("Version: devel")
			} else {
				fmt.Printf("Version: %s\n", version)
			}
		},
	}
}
