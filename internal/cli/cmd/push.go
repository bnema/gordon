package cmd

import (
	"fmt"
	"os"
	"regexp"

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

			if resp.StatusCode != 200 {
				fmt.Println("Unexpected status code:", resp.StatusCode)
				return
			}

			fmt.Println(resp.Body)
		},
	}

	// Add flags
	pushCmd.Flags().StringVarP(&port, "port", "p", "", "Port mapping for the container")
	pushCmd.Flags().StringVarP(&targetDomain, "target", "t", "", "Target domain for Traefik")

	return pushCmd
}
