package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/cli/handler"
	"github.com/bnema/gordon/pkg/humanize"

	"github.com/spf13/cobra"
)

func NewPingCommand(a *cli.App) *cobra.Command {
	//	handler.FieldCheck(a)
	return &cobra.Command{
		Use:   "ping",
		Short: "Send a ping request to the backend",
		PreRun: func(cmd *cobra.Command, args []string) {
			if err := handler.FieldCheck(a); err != nil {
				fmt.Println("Field check failed:", err)
				os.Exit(1)
			}
		},
		Run: func(cmd *cobra.Command, args []string) {
			pingResp, err := handler.PerformPingRequest(a)
			if err != nil {
				fmt.Println("Error:", err)
				return
			}

			// Parse the uptime duration string
			duration, err := time.ParseDuration(pingResp.Uptime)
			if err != nil {
				fmt.Println("Error parsing uptime duration:", err)
				return
			}

			// Calculate the start time of the server
			startTime := time.Now().Add(-duration)

			// Use humanize to get a human-readable representation of the start time
			humanizedUptime := humanize.TimeAgo(startTime)

			// Print the information
			fmt.Printf("Pong!\nServer up since: %s\nServer version: %s\n", humanizedUptime, pingResp.Version)
		},
	}
}
