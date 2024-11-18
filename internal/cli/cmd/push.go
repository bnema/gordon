package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/cli/handler"
	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/pkg/docker"
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
	// Check authentication early
	if err := handler.CheckAndRefreshAuth(a); err != nil {
		log.Error("Authentication check failed", "error", err)
		return err
	}

	if err := handler.ValidateImageName(imageName); err != nil {
		return fmt.Errorf("invalid image name: %w", err)
	}

	handler.EnsureImageTag(&imageName)

	reader, actualSize, err := handler.ExportDockerImage(imageName)
	if err != nil {
		return fmt.Errorf("error exporting image: %w", err)
	}
	defer reader.Close()

	_, err = docker.GetImageIDByName(imageName)
	if err != nil {
		return fmt.Errorf("failed to get image ID: %w", err)
	}

	sizeInMB := float64(actualSize) / 1024 / 1024
	log.Info("Image exported successfully",
		"image", imageName,
		"size", fmt.Sprintf("%.2fMB", sizeInMB))

	var resp *handler.Response

	// Use chunked endpoint if image is larger than 10MB
	if sizeInMB > 10 {
		chunkedClient := handler.NewChunkedClient(a)
		if chunkedClient == nil {
			return fmt.Errorf("failed to initialize chunked client - authentication issue")
		}

		ctx := context.Background()
		headers := make(http.Header)
		// Add any necessary headers here
		headers.Set("Content-Type", "application/octet-stream")

		var err error
		resp, err = chunkedClient.SendFileAsChunks(ctx, "/push/chunked", headers, reader, actualSize, imageName)
		if err != nil {
			return fmt.Errorf("chunked transfer failed: %w", err)
		}
	} else {
		// Use regular push endpoint for smaller images
		reqPayload := common.RequestPayload{
			Type: "push",
			Payload: common.PushPayload{
				ImageName: imageName,
				Data:      reader,
			},
		}
		var err error
		resp, err = handler.SendHTTPRequest(a, &reqPayload, "POST", "/push")
		if err != nil {
			return fmt.Errorf("regular push failed: %w", err)
		}
	}

	if resp == nil {
		return fmt.Errorf("received nil response from server")
	}

	var pushResponse common.PushResponse
	if err := json.Unmarshal(resp.Body, &pushResponse); err != nil {
		log.Error("Error parsing response", "error", err)
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !pushResponse.Success {
		log.Error("Push failed", "resp", pushResponse.Message)
		return fmt.Errorf(pushResponse.Message)
	}

	// Remove the double quotes from the message
	message := strings.Trim(pushResponse.Message, "\"")
	log.Info(message)

	if pushResponse.CreateContainerURL != "" {
		log.Info("Container creation available", "url", pushResponse.CreateContainerURL)
	}

	return nil
}
