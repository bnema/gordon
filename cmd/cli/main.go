package main

import (
	"fmt"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/cli/cmd"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{Use: "gordon"}

func InitializeCommands(a *cli.App) {
	// rootCmd.AddCommand(cmd.NewHelloCommand(a))
	rootCmd.AddCommand(cmd.NewPingCommand(a))
}

func Execute(a *cli.App) {
	InitializeCommands(a)
	cobra.CheckErr(rootCmd.Execute())
}

func main() {
	a, err := cli.NewClientApp()
	if err != nil {
		fmt.Println("Error initializing app:", err)
	}
	Execute(a)
}
