package cmd

import (
	"fmt"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/cli/handler"
	"github.com/bnema/gordon/internal/common"
	"github.com/spf13/cobra"
)

func NewHelloCommand(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "hello",
		Short: "Send a hello request to the backend",
		Run: func(cmd *cobra.Command, args []string) {
			data := map[string]interface{}{
				"message": "Hello, World!",
			}
			payload, err := common.CreatePayload("hello", data)
			if err != nil {
				fmt.Println("Error creating payload:", err)
				return
			}

			// Use your handler.SendHTTPRequest function here
			// Assuming that the SendHTTPRequest is moved to a package that can be imported here
			handler.SendHTTPRequest(a, payload)

			fmt.Printf("Sending payload to backend: %+v\n", payload)
		},
	}
}
