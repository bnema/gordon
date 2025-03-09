package cmd

import (
	"fmt"
	"time"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/cli/cmd"
	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/server"
	"github.com/spf13/cobra"

	_ "github.com/joho/godotenv/autoload"
)

var (
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
	rootCmd.AddCommand(cmd.NewVersionCommand(client))
	rootCmd.AddCommand(cmd.NewProxyCommand(server))
}

func Execute(client *cli.App, server *server.App) {
	InitializeCommands(client, server)
	cobra.CheckErr(rootCmd.Execute())
}

func ExecuteCLI(build, commit, date string) {
	versionInfo := common.GetVersionInfo(build, commit, date, proxyURL)

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

	// Start periodic version checking in the background
	go common.CheckVersionPeriodically(versionInfo, 3*time.Hour)

	Execute(a, s)
}
