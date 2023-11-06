package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/cli/handler"
	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/pkg/humanize"

	"github.com/spf13/cobra"
)

// Define a struct to match the JSON response structure
type PingResponse struct {
	Uptime  string `json:"uptime"`
	Version string `json:"version"`
}

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
			data := map[string]interface{}{"message": "ping"}
			payload, err := common.NewPingPayload(data)
			if err != nil {
				fmt.Println("Error creating payload:", err)
				return
			}

			// Create a RequestPayload and populate it
			reqPayload := common.RequestPayload{
				Type:    "ping",
				Payload: payload,
			}

			resp, err := handler.SendHTTPRequest(a, &reqPayload, "GET", "/ping")
			if err != nil {
				fmt.Println("Error sending HTTP request:", err)
				return
			}

			if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 {
				fmt.Println("Login incorrect, edit your config file and try again")
				return
			}

			// Unmarshal the JSON response into the PingResponse struct
			var pingResp PingResponse
			err = json.Unmarshal(resp.Body, &pingResp)
			if err != nil {
				fmt.Println("Error unmarshalling response:", err)
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

			/// Print the information
			fmt.Printf("Pong!\nServer up since: %s\nServer version: %s\n", humanizedUptime, pingResp.Version)

		},
	}
}
