package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/cli/handler"
	"github.com/bnema/gordon/internal/common"
	"github.com/charmbracelet/log"
	"github.com/spf13/cobra"
)

func init() {
	log.SetReportTimestamp(true)
	log.SetTimeFormat("15:04")
}

func NewPushCommand(a *cli.App) *cobra.Command {
	var port string

	pushCmd := &cobra.Command{
		Use:   "push [image:tag]",
		Short: "Push an image to your remote Gordon instance",
		Args:  cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			if err := handler.FieldCheck(a); err != nil {
				log.Error("Field check failed", "error", err)
				os.Exit(1)
			}
		},
		Run: func(cmd *cobra.Command, args []string) {
			imageName := args[0]
			log.Info("Pushing image", "image", imageName)

			if err := pushImage(a, imageName); err != nil {
				log.Error("Push failed", "error", err)
				os.Exit(1)
			}
		},
	}

	pushCmd.Flags().StringVarP(&port, "port", "p", "", "Port mapping for the container (e.g., 8080:80/tcp)")

	return pushCmd
}

func pushImage(a *cli.App, imageName string) error {
	if err := handler.ValidateImageName(imageName); err != nil {
		return fmt.Errorf("invalid image name: %w", err)
	}

	handler.EnsureImageTag(&imageName)

	reader, actualSize, err := exportDockerImage(imageName)
	if err != nil {
		return fmt.Errorf("error exporting image: %w", err)
	}
	defer reader.Close()

	sizeInMB := float64(actualSize) / 1024 / 1024

	log.Info("Image exported successfully", "image", imageName, "size", fmt.Sprintf("%.2fMB", sizeInMB))

	reqPayload := common.RequestPayload{
		Type: "push",
		Payload: common.PushPayload{
			ImageName: imageName,
			Data:      reader,
		},
	}

	resp, err := handler.SendHTTPRequest(a, &reqPayload, "POST", "/push")
	if err != nil {
		return fmt.Errorf("error sending HTTP request: %w", err)
	}

	var pushResponse common.PushResponse
	if err := json.Unmarshal(resp.Body, &pushResponse); err != nil {
		return fmt.Errorf("error parsing response: %w", err)
	}

	if !pushResponse.Success {
		return fmt.Errorf("push failed: %s", pushResponse.Message)
	}

	log.Info("Image pushed successfully", "image", imageName)

	if pushResponse.CreateContainerURL != "" {
		log.Info("Container creation URL:", "url", pushResponse.CreateContainerURL)
	}

	return nil
}
