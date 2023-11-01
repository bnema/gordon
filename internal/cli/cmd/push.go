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

			// Check if the image name is valid
			match, _ := regexp.MatchString("^([a-zA-Z0-9_\\-\\.]+\\/)*[a-zA-Z0-9_\\-\\.]+(:[a-zA-Z0-9_\\-\\.]+)?$", imageName)
			if !match {
				fmt.Println("You must specify a valid image name in the form (registry/)image:tag, check your container engine image list")
				return
			}

			// If there is no :tag at the end of the image name, we append :latest
			if !strings.Contains(imageName, ":") {
				imageName += ":latest"
			}

			// Check the ports struct port:port (proto is optional)
			match, _ = regexp.MatchString("^[0-9]+:[0-9]+(\\/(tcp|udp))?$", port)
			if !match {
				fmt.Println("You must specify a port mapping in the form port:port/proto, if no protocol is specified, TCP is used")
				return
			}

			// Check the target domain struct http(s)://domain.tld (proto is optional)
			match, _ = regexp.MatchString("^(https?:\\/\\/)?([a-zA-Z0-9\\-_\\.]+\\.)+[a-zA-Z0-9\\-_\\.]+(:[0-9]+)?$", targetDomain)
			if !match {
				fmt.Println("You must specify a valid target domain in the form http(s)://domain.tld, if no protocol is specified, HTTPS is used")
				return
			}

			// Get the image ID
			imageID, err := docker.GetImageID(imageName)
			if err != nil {
				fmt.Println("Error getting image ID:", err)
				return
			}

			// Get the image size
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
			fmt.Println("Container is running !")
			// Initialize counter for retries
			retryCount := 0
			maxRetries := 40
			progressIndicator := ""

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

			// Check URL availability
			for {
				// Making GET request to the target domain
				_, err := client.Get(targetDomain)
				if err != nil {
					retryCount++
					if retryCount >= maxRetries {
						fmt.Println("\nImpossible to access the domain. Are you sure it is correct and that Traefik recognizes it?")
						break
					}
				} else {
					fmt.Println("\nDomain is available at:", targetDomain)
					break
				}

				// Update the progress indicator
				progressIndicator += "."

				// Limit the progress indicator to 5 dots
				if len(progressIndicator) > 5 {
					flush := "\r"
					for i := 0; i < len(progressIndicator); i++ {
						flush += " "
					}
				}

				// Use \r to move the cursor to the beginning of the line
				fmt.Printf("\r%s%s", "Wait while Traefik is setting up the domain and certificate", progressIndicator)

				// Wait for 1 second before the next check
				time.Sleep(time.Second)
			}

		},
	}

	// Add flags
	pushCmd.Flags().StringVarP(&port, "port", "p", "", "Port mapping for the container")
	pushCmd.Flags().StringVarP(&targetDomain, "target", "t", "", "Target domain for Traefik")

	return pushCmd
}
