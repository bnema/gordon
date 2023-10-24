package cli

import (
	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/cli/cmd"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{Use: "gordon"}

func InitializeCommands(a *app.App) {
	// rootCmd.AddCommand(cmd.NewHelloCommand(a))
	rootCmd.AddCommand(cmd.NewPingCommand(a))
}

func Execute(a *app.App) {
	InitializeCommands(a)
	cobra.CheckErr(rootCmd.Execute())
}
