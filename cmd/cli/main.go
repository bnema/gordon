package main

import (
	"fmt"
	"regexp"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/cli/cmd"
	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/spf13/cobra"
)

var build string

var rootCmd = &cobra.Command{Use: "gordon"}

func InitializeCommands(a *cli.App, s *server.App) {
	rootCmd.AddCommand(cmd.NewServeCommand(s))
	rootCmd.AddCommand(cmd.NewPingCommand(a))
	rootCmd.AddCommand(cmd.NewPushCommand(a))
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

	common.DockerInit(&s.Config.ContainerEngine)

	// build looks like this: "0.0.901-b98a337"
	//use regex to only get the version number
	build = regexp.MustCompile(`\d+\.\d+\.\d+`).FindString(build)
	// Set the BuildVersion
	s.Config.Build.BuildVersion = build

	inContainer, err := docker.IsRunningInContainer()
	if err != nil {
		fmt.Printf("Error checking if running in a container: %s\n", err)
	} else if inContainer {
		fmt.Println("Running inside a container.")
	} else {
		fmt.Println("Not running inside a container.")
	}
	Execute(a, s)
}
