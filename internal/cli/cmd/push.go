package cmd

import (
	"bytes"
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
	"github.com/bnema/gordon/internal/common"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

func NewPushCommand(a *cli.App) *cobra.Command {
	pushCmd := &cobra.Command{
		Use:   "push [image:tag]",
		Short: "Push an image to your remote Gordon instance",
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

			// Export the image to a reader and return its true size
			reader, _, successMsg, err := exportDockerImage(imageName)
			if err != nil {
				fmt.Println("Error exporting image:", err)
				return
			}

			fmt.Println(successMsg)

			// Create a progress bar
			progressBar := cmd.OutOrStdout()
			fmt.Fprintf(progressBar, "Uploading image... ")

			// Create a RequestPayload and populate it
			reqPayload := common.RequestPayload{
				Type: "push",
				Payload: common.PushPayload{
					ImageName: imageName,
					Data:      reader,
				},
			}

			var wg sync.WaitGroup
			wg.Add(1)

			go func() {
				defer wg.Done()
				resp, err := handler.SendHTTPRequest(a, &reqPayload, "POST", "/push")
				if err != nil {
					fmt.Println("Error sending request:", err)
					return
				}

				if resp.StatusCode != http.StatusOK {
					bodyBytes, _ := io.ReadAll(bytes.NewReader(resp.Body))
					fmt.Fprintln(cmd.OutOrStderr(), "Server returned an error:", string(bodyBytes))
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
					Timeout: 10 * time.Second,
				}

				// Only set TLS config if target is HTTPS
				if isHTTPS {
					client.Transport = &http.Transport{
						TLSClientConfig: &tls.Config{InsecureSkipVerify: false}, // set false to ensure certificate is validated
					}
				}

			}()

			// Wait for the HTTP request to finish
			wg.Wait()

			// Close the reader
			err = reader.Close()
			if err != nil {
				fmt.Println("Error closing reader:", err)
				return
			}
		},
	}

	return pushCmd
}
