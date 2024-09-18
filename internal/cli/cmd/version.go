package cmd

import (
	"fmt"

	"github.com/bnema/gordon/internal/cli"
	"github.com/spf13/cobra"
)

func NewVersionCommand(a *cli.App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Affiche la version de Gordon",
		Run: func(cmd *cobra.Command, args []string) {
			version := a.Config.GetVersion()
			if version == "" {
				fmt.Println("Version : devel")
			} else {
				fmt.Printf("Version : %s\n", version)
			}
		},
	}
}
