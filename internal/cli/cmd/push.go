package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

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

			if err := validatePushInputs(imageName); err != nil {
				log.Error("Validation failed", "error", err)
				return
			}

			reader, actualSize, err := handler.ExportDockerImage(imageName)
			if err != nil {
				log.Error("Error exporting image", "error", err)
				return
			}
			defer reader.Close()

			sizeInMB := float64(actualSize) / 1024 / 1024

			log.Info("Image exported successfully", "image", imageName, "size", fmt.Sprintf("%.2fMB", sizeInMB))

			if err := pushImage(a, reader, imageName); err != nil {
				os.Exit(1)
			}
		},
	}

	pushCmd.Flags().StringVarP(&port, "port", "p", "", "Port mapping for the container (e.g., 8080:80/tcp)")

	return pushCmd
}

func pushImage(a *cli.App, reader io.Reader, imageName string) error {
	// Check authentication early
	if err := handler.CheckAndRefreshAuth(a); err != nil {
		log.Error("Authentication check failed", "error", err)
		return err
	}

	var buf bytes.Buffer
	size, err := io.Copy(&buf, reader)
	if err != nil {
		return fmt.Errorf("failed to read image data: %w", err)
	}

	imageReader := bytes.NewReader(buf.Bytes())
	sizeInMB := float64(size) / 1024 / 1024
	log.Info("Attempting to push...",
		"image", imageName,
		"size", fmt.Sprintf("%.2fMB", sizeInMB),
	)

	headers := http.Header{
		"X-Image-Name": {imageName},
		"Content-Type": {"application/octet-stream"},
	}

	chunkedClient := handler.NewChunkedClient(a)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	resp, err := chunkedClient.SendFileAsChunks(ctx, "/push/chunked", headers, imageReader, size, imageName)
	if err != nil {
		log.Error("Failed to send chunks", "error", err)
		return fmt.Errorf("failed to send chunks: %w", err)
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

func validatePushInputs(imageName string) error {
	if err := handler.ValidateImageName(imageName); err != nil {
		return err
	}
	return handler.EnsureImageTag(&imageName)
}
