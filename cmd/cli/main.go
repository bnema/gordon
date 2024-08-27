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

	// env auto load
	_ "github.com/joho/godotenv/autoload"
)

var (
	build    string
	commit   string
	date     string
	rootCmd  = &cobra.Command{Use: "gordon"}
	proxyURL = "https://gordon-proxy.bamen.dev"
)

func InitializeCommands(client *cli.App, server *server.App) {
	rootCmd.AddCommand(cmd.NewRootCommand(client))
	rootCmd.AddCommand(cmd.NewServeCommand(server))
	rootCmd.AddCommand(cmd.NewPingCommand(client))
	rootCmd.AddCommand(cmd.NewDeployCommand(client))
	rootCmd.AddCommand(cmd.NewUpdateCommand(client))
	rootCmd.AddCommand(cmd.NewPushCommand(client))
}

func Execute(client *cli.App, server *server.App) {
	InitializeCommands(client, server)
	cobra.CheckErr(rootCmd.Execute())
}

func main() {
	build = regexp.MustCompile(`\d+\.\d+\.\d+`).FindString(build)
	buildInfo := &common.BuildConfig{
		BuildVersion: build,
		BuildCommit:  commit,
		BuildDate:    date,
		ProxyURL:     proxyURL,
	}

	a, err := cli.NewClientApp(buildInfo)
	if err != nil {
		fmt.Println("Error initializing app:", err)
	}
	s, err := server.NewServerApp(buildInfo)
	if err != nil {
		fmt.Println("Error initializing app:", err)
	}

	common.DockerInit(&s.Config.ContainerEngine)

	// Check for new version
	go func() {
		msg, err := common.CheckVersionPeriodically(&s.Config)
		if err != nil || msg != "" {
			log.Println(msg)
		}
	}()

	Execute(a, s)
}
