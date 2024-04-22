package cmd

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/cli/handler"
	"github.com/bnema/gordon/internal/cli/mvu"
	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/pkg/docker"
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
			color.White("Pushing image: %s", imageName)

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
			reader, actualSize, successMsg, err := exportDockerImage(imageName)
			if err != nil {
				fmt.Println("Error exporting image:", err)
				return
			}

			fmt.Println(successMsg)

			progressCh := make(chan mvu.ProgressMsg)
			errCh := make(chan error, 1) // Buffer of 1 to prevent goroutine leak in case of non-blocking send

			// Create a progress function to update the progress bar
			progressReader := &mvu.ProgressReader{
				Reader:     reader,     // This is the actual reader from exportDockerImage
				Total:      actualSize, // This is the total size of the data to be read
				ProgressCh: progressCh, // This is the channel used to send progress updates
			}

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

			var wg sync.WaitGroup
			wg.Add(1)

			go func() {
				defer wg.Done()
				resp, err := handler.SendHTTPRequest(a, &reqPayload, "POST", "/push")
				if err != nil {
					errCh <- fmt.Errorf("error sending HTTP request: %w", err)
					return
				}

				// Check the response
				targetDomain := string(resp.Body)
				targetDomain = strings.TrimSpace(targetDomain)  // Remove leading and trailing whitespace
				targetDomain = strings.Trim(targetDomain, "\"") // Remove leading and trailing quotes
				// Close the progress channel after the upload is complete
				// Determine if the target is HTTPS or HTTP
				isHTTPS := strings.HasPrefix(targetDomain, "https://")

				// Initialize HTTP client
				client := &http.Client{
					Timeout: 60 * time.Second,
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
					errCh <- fmt.Errorf("error running deployment TUI: %w", err)
					return
				}

				// Check if the deployment was successful
				if !finalModel.DeploymentDone {
					errCh <- fmt.Errorf("deployment failed")
					return
				}

				// Print the final message
				color.Blue("Deployment successful!")
				fmt.Println("Your application is now available at:", targetDomain)
				close(progressCh)
			}()
			// Run the progress bar TUI
			m, err := mvu.RunProgressBarTUI(progressCh)
			if err != nil {
				errCh <- fmt.Errorf("error running progress bar TUI: %w", err)
				return
			}
			// Wait for the deployment goroutine to complete
			wg.Wait()

			// Check for errors from the deployment goroutine
			select {
			case err := <-errCh:
				if err != nil {
					fmt.Println(err)
					return
				}
			default:
			}

			// Check if the progress bar is done
			if m.Done {
				// Close the reader
				err = progressReader.Close()
				if err != nil {
					fmt.Println("Error closing reader:", err)
					return
				}
			} else {
				fmt.Println("Deployment completed, but progress bar is not done.")
			}
		},
	}

	// Add flags
	deployCmd.Flags().StringVarP(&port, "port", "p", "", "Port mapping for the container")
	deployCmd.Flags().StringVarP(&targetDomain, "target", "t", "", "Target domain for Traefik")

	return deployCmd
}

// export the docker image to a reader and return its true size
func exportDockerImage(imageName string) (io.ReadCloser, int64, string, error) {
	// Check if what the user submitted is a valid image ID
	exists, err := docker.CheckIfImageExists(imageName)
	if err != nil {
		return nil, 0, "", fmt.Errorf("error while checking image existence: %w", err)
	}

	var imageID string
	if exists {
		// What the user submitted is a valid image ID
		imageID = imageName
	} else {
		// What the user submitted is not a valid image ID, search by name
		imageID, err = docker.GetImageIDByName(imageName)
		if err != nil {
			return nil, 0, "", fmt.Errorf("error while searching for image by name: %w", err)
		}
	}

	actualSize, err := docker.GetImageSizeFromReader(imageID)
	if err != nil {
		return nil, 0, "", fmt.Errorf("error while retrieving image size: %w", err)
	}

	reader, err := docker.ExportImageFromEngine(imageID)
	if err != nil {
		return nil, 0, "", fmt.Errorf("error while exporting image: %w", err)
	}

	// Create a success message
	successMsg := fmt.Sprintf("Image %s exported successfully", imageName)

	// Return the reader, actual size, and success message
	return reader, actualSize, successMsg, nil
}
