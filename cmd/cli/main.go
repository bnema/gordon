package main

import (
	"fmt"
	"log"
	"os"
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
	build  string
	commit string
	date   string
)

var rootCmd = &cobra.Command{Use: "gordon"}

func InitializeCommands(client *cli.App, server *server.App) {
	rootCmd.AddCommand(cmd.NewRootCommand(client))
	rootCmd.AddCommand(cmd.NewServeCommand(server))
	rootCmd.AddCommand(cmd.NewPingCommand(client))
	rootCmd.AddCommand(cmd.NewDeployCommand(client))
	rootCmd.AddCommand(cmd.NewUpdateCommand(client))
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
		ProxyURL:     os.Getenv("PROXY_URL"),
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
