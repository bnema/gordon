package main

import (
	"fmt"
	"log"
	"regexp"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/cli/cmd"
	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/server"
	"github.com/spf13/cobra"
)

var (
	build  string
	commit string
	date   string
)

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

	build = regexp.MustCompile(`\d+\.\d+\.\d+`).FindString(build)
	s.Config.Build = common.BuildConfig{
		BuildVersion: build,
		BuildCommit:  commit,
		BuildDate:    date,
		ProxyURL:     "https://gordon-proxy.bnema.dev",
	}

	fmt.Printf("Gordon version %\n", s.Config.Build.BuildVersion)
	go func() {
		msg, err := common.CheckVersionPeriodically(&s.Config)
		if err != nil {
			log.Println(err)
		}

		if msg != "" {
			log.Println(msg)
		}

	}()

	Execute(a, s)
}
