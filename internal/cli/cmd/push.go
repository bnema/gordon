package cmd

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/cli/handler"
	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/cheggaaa/pb"
	"github.com/spf13/cobra"
)

func NewPushCommand(a *cli.App) *cobra.Command {
	var port string
	var targetDomain string

	pushCmd := &cobra.Command{
		Use:   "push [image:tag]",
		Short: "Push an image to the server, if no tag is specified, latest is used",
		Args:  cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			if err := handler.FieldCheck(a); err != nil {
				fmt.Println("Field check failed:", err)
				os.Exit(1)
			}
		},
		Run: func(cmd *cobra.Command, args []string) {
			imageName := args[0]
			fmt.Println("Exporting image:", imageName)

			// Check if the image name is valid or if a tag is specified
			match, _ := regexp.MatchString(`^([a-zA-Z0-9\-_.]+\/)?[a-zA-Z0-9\-_.]+(:[a-zA-Z0-9\-_.]+)?$`, imageName)
			if !match {
				fmt.Println("Invalid image name or no tag specified")
				return
			}

			imageID, err := docker.GetImageID(imageName)
			if err != nil {
				fmt.Println("Error getting image ID:", err)
				return
			}

			totalSize, err := docker.GetImageSize(imageID)
			if err != nil {
				fmt.Println("Error estimating image size:", err)
				return
			}

			// Create a new progress bar
			bar := pb.New64(totalSize).SetUnits(pb.U_BYTES)
			reader, err := docker.ExportImageFromEngine(imageID)
			if err != nil {
				fmt.Println("Error exporting image:", err)
				return
			}
			// Wrap the original reader with progress bar reader
			progressReader := bar.NewProxyReader(reader)

			// Create a RequestPayload and populate it
			reqPayload := common.RequestPayload{
				Type: "push",
				Payload: common.PushPayload{
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
			// Stop the progress bar
			bar.Finish()

			// Check the response
			targetDomain := string(resp.Body)
			targetDomain = strings.TrimSpace(targetDomain)  // Remove leading and trailing whitespace
			targetDomain = strings.Trim(targetDomain, "\"") // Remove leading and trailing quotes

			// Notify user
			fmt.Println("Wait while Traefik is setting up the domain and certificate...")
			progressIndicator := ""
			// Check URL availability
			for {
				client := &http.Client{
					Timeout: time.Second * 20,
					Transport: &http.Transport{
						TLSClientConfig: &tls.Config{InsecureSkipVerify: false}, // set false to ensure certificate is validated
					},
				}

				// Making GET request to the target domain
				resp, err := client.Get(targetDomain)
				if err != nil {
					// we do nothing here, we just wait for the domain to be available

				} else {
					fmt.Println("Domain is available at:", targetDomain)
					break
				}

				// Close the response body, if non-nil
				if resp != nil {
					resp.Body.Close()
				}

				// Update and print the progress indicator
				progressIndicator += "."

				// Limit the progress indicator to 5 dots
				if len(progressIndicator) > 5 {
					progressIndicator = ""
				}

				fmt.Printf("\r%s", progressIndicator)

				// Wait for 1 secondsbefore next check
				time.Sleep(time.Second)
			}
		},
	}

	// Add flags
	pushCmd.Flags().StringVarP(&port, "port", "p", "", "Port mapping for the container")
	pushCmd.Flags().StringVarP(&targetDomain, "target", "t", "", "Target domain for Traefik")

	return pushCmd
}
