package cmd

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/cli/handler"
	"github.com/bnema/gordon/internal/cli/mvu"
	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/cheggaaa/pb"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

func NewDeployCommand(a *cli.App) *cobra.Command {
	var port string
	var targetDomain string

	deployCmd := &cobra.Command{
		Use:   "deploy [image:tag]",
		Short: "Deploy an image to your remote Gordon instance",
		Args:  cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			if err := handler.FieldCheck(a); err != nil {
				fmt.Println("Field check failed:", err)
				os.Exit(1)
			}
		},
		Run: func(cmd *cobra.Command, args []string) {
			imageName := args[0]
			color.Blue("Deploying image: %s", imageName)

			// Validate the image name
			if err := handler.ValidateImageName(imageName); err != nil {
				fmt.Println(err)
				return
			}

			// Ensure the image has a tag
			handler.EnsureImageTag(&imageName)

			// Validate the port mapping
			if err := handler.ValidatePortMapping(port); err != nil {
				fmt.Println(err)
				return
			}

			// Validate the target domain
			if err := handler.ValidateTargetDomain(targetDomain); err != nil {
				fmt.Println(err)
				return
			}

			// Export the image to a reader and return its true size
			reader, actualSize, err := exportDockerImage(imageName)
			if err != nil {
				fmt.Println("Error exporting image:", err)
				return
			}

			// Initialize the progress bar
			bar := pb.New64(actualSize)

			// Wrap the ReadCounter with the bar proxy
			progressReader := bar.NewProxyReader(reader)

			// Create a RequestPayload and populate it
			reqPayload := common.RequestPayload{
				Type: "deploy",
				Payload: common.DeployPayload{
					Ports:        port,
					TargetDomain: targetDomain,
					ImageName:    imageName,
					Data:         progressReader,
				},
			}

			// Start the progress bar
			bar.Start()
			// Send the request to the backend
			resp, err := handler.SendHTTPRequest(a, &reqPayload, "POST", "/push")
			if err != nil {
				fmt.Println("Error sending HTTP request:", err)
				return
			}
			// Before finishing, ensure progress does not exceed 100%
			bar.Set64(actualSize)

			// Stop the progress bar
			bar.Finish()

			// Check the response
			targetDomain := string(resp.Body)
			targetDomain = strings.TrimSpace(targetDomain)  // Remove leading and trailing whitespace
			targetDomain = strings.Trim(targetDomain, "\"") // Remove leading and trailing quotes

			// Determine if the target is HTTPS or HTTP
			isHTTPS := strings.HasPrefix(targetDomain, "https://")

			// Initialize HTTP client
			client := &http.Client{
				Timeout: 5 * time.Second,
			}

			// Only set TLS config if target is HTTPS
			if isHTTPS {
				client.Transport = &http.Transport{
					TLSClientConfig: &tls.Config{InsecureSkipVerify: false}, // set false to ensure certificate is validated
				}
			}

			// Run the TUI program
			finalModel, err := mvu.RunDeploymentTUI(client, imageName, targetDomain, port)
			if err != nil {
				fmt.Println("Error running deployment TUI:", err)
				return
			}

			// Check if the deployment was successful
			if !finalModel.DeploymentDone {
				fmt.Println("Deployment failed check your configuration and try again.")
				return
			}

			// Print the final message
			color.Green("	Deployment successful!")
			fmt.Println("	Your application is now available at:", targetDomain)
		},
	}

	// Add flags
	deployCmd.Flags().StringVarP(&port, "port", "p", "", "Port mapping for the container")
	deployCmd.Flags().StringVarP(&targetDomain, "target", "t", "", "Target domain for Traefik")

	return deployCmd
}

// export the docker image to a reader and return its true size
func exportDockerImage(imageName string) (io.ReadCloser, int64, error) {
	imageID, err := docker.GetImageID(imageName)
	if err != nil {
		return nil, 0, fmt.Errorf("error getting image ID: %w", err)
	}

	actualSize, err := docker.GetImageSizeFromReader(imageID)
	if err != nil {
		return nil, 0, fmt.Errorf("error getting image size: %w", err)
	}

	reader, err := docker.ExportImageFromEngine(imageID)
	if err != nil {
		return nil, 0, fmt.Errorf("error exporting image: %w", err)
	}

	// Return the wrapped reader.
	return reader, actualSize, nil
}
