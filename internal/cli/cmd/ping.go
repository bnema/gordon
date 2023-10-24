package cmd

import (
	"fmt"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/cli/handler"
	"github.com/bnema/gordon/internal/common"
	"github.com/spf13/cobra"
)

func NewPingCommand(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "ping",
		Short: "Send a ping request to the backend",
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

			resp, err := handler.SendHTTPRequest(a, &reqPayload, "/ping")
			if err != nil {
				fmt.Println("Error sending HTTP request:", err)
				return
			}

			fmt.Print(string(resp.Body))
		},
	}
}
