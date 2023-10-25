package main

import (
	"fmt"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/cli/cmd"
	"github.com/bnema/gordon/internal/server"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{Use: "gordon"}

func InitializeCommands(a *cli.App, s *server.App) {
	rootCmd.AddCommand(cmd.NewServeCommand(s))
	rootCmd.AddCommand(cmd.NewPingCommand(a))
}

func Execute(a *cli.App, s *server.App) {
	InitializeCommands(a, s)
	cobra.CheckErr(rootCmd.Execute())
}

func main() {
	a, err := cli.NewClientApp()
	if err != nil {
		fmt.Println("Error initializing app:", err)
	}
	s, err := server.NewServerApp()
	if err != nil {
		fmt.Println("Error initializing app:", err)
	}

	Execute(a, s)
}
