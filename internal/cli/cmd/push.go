package cmd

import (
	"fmt"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/cli/handler"
	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/spf13/cobra"
)

// the push command is used to push a container image to gordon's endpoint

// Pseudo code of the steps for the push command

// 1. Extract the container image as .tar and store it in a temporary directory

// 2. Prepare a payload with the .tar as a byte array, the image name and tag (if any)

// 3. append the payload to the request body with the type "push" and the token

// 4. Send the request to the backend

// 5. If the response is 200, print the success message

func NewPushCommand(a *cli.App) *cobra.Command {
	var port string
	var targetDomain string

	handler.FieldCheck(a)

	pushCmd := &cobra.Command{
		Use:   "push [image:tag]",
		Short: "Push an image to the server, if no tag is specified, latest is used",
		Args:  cobra.ExactArgs(1), // Require exactly one positional argument for image:tag
		Run: func(cmd *cobra.Command, args []string) {
			imageName := args[0]
			fmt.Println("Exporting image:", imageName)
			// Export the image using Docker client
			data, err := docker.ExportImageFromEngine(imageName)
			if err != nil {
				fmt.Println("Error exporting image:", err)
				return
			}
			fmt.Println("Image exported successfully")
			// Create a RequestPayload and populate it
			payload := common.RequestPayload{
				Type: "push",
				Payload: common.PushPayload{
					Ports:        port,
					TargetDomain: targetDomain,
					ImageName:    imageName,
					Data:         data,
				},
			}
			fmt.Println("Sending image to backend")
			// Send the request to the backend
			resp, err := handler.SendHTTPRequest(a, &payload, "/push")
			if err != nil {
				fmt.Println("Error sending HTTP request:", err)
				return
			}

			fmt.Print(string(resp.Body))
		},
	}

	// Add flags
	pushCmd.Flags().StringVarP(&port, "port", "p", "", "Port mapping for the container")
	pushCmd.Flags().StringVarP(&targetDomain, "target", "t", "", "Target domain for Traefik")

	return pushCmd
}
