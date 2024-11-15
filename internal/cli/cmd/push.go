package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
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

	reader, actualSize, err := handler.ExportDockerImage(imageName)
	if err != nil {
		return fmt.Errorf("error exporting image: %w", err)
	}
	defer reader.Close()

	sizeInMB := float64(actualSize) / 1024 / 1024
	log.Info("Image exported successfully",
		"image", imageName,
		"size", fmt.Sprintf("%.2fMB", sizeInMB))

	headers := http.Header{
		"X-Image-Name": {imageName},
		"Content-Type": {"application/octet-stream"},
	}

	chunkedClient := handler.NewChunkedClient(a)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	resp, err := chunkedClient.SendFile(ctx, "/push", headers, reader, actualSize, imageName)
	if err != nil {
		var pushErr *common.DeploymentError
		if errors.As(err, &pushErr) {
			return fmt.Errorf(pushErr.Message)
		}
		return fmt.Errorf("chunked transfer failed: %w", err)
	}

	fmt.Println("Push response:", resp)

	log.Info("Image pushed and imported successfully", "image", imageName)

	return nil
}
