// gordon/internal/cli/cmd/deploy.go

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func NewDeployCommand(a *cli.App) *cobra.Command {
	var port string
	var targetDomain string

	deployCmd := &cobra.Command{
		Use:   "deploy [image:tag]",
		Short: "Deploy an image to your remote Gordon instance",
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

			if err := validateInputs(imageName, port, targetDomain); err != nil {
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

			if err := deployImage(a, reader, imageName, port, targetDomain); err != nil {
				os.Exit(1)
			}
		},
	}

	deployCmd.Flags().StringVarP(&port, "port", "p", "", "Port mapping for the container and Traefik entry point")
	deployCmd.Flags().StringVarP(&targetDomain, "target", "t", "", "Target domain for Traefik")

	return deployCmd
}

// Client side - deploy.go

func deployImage(a *cli.App, reader io.Reader, imageName, port, targetDomain string) error {
	// Create a buffer to store the entire image data
	var buf bytes.Buffer
	size, err := io.Copy(&buf, reader)
	if err != nil {
		return fmt.Errorf("failed to read image data: %w", err)
	}

	// Create a new reader from the buffer
	imageReader := bytes.NewReader(buf.Bytes())

	sizeInMB := float64(size) / 1024 / 1024
	log.Info("Attempting to deploy...",
		"image", imageName,
		"size", fmt.Sprintf("%.2fMB", sizeInMB),
		"port", port,
		"target", targetDomain,
	)

	headers := http.Header{
		"X-Image-Name":    {imageName},
		"X-Ports":         {port},
		"X-Target-Domain": {targetDomain},
		"Content-Type":    {"application/octet-stream"},
	}

	chunkedClient := handler.NewChunkedClient(a)
	var finalResponse *common.DeployResponse

	for {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
		defer cancel()

		resp, err := chunkedClient.SendFile(ctx, "/deploy", headers, imageReader, size, imageName)
		if err != nil {
			var deployErr *common.DeploymentError
			if errors.As(err, &deployErr) && deployErr.StatusCode == http.StatusConflict {
				var deployResponse common.DeployResponse
				if jsonErr := json.Unmarshal([]byte(deployErr.RawResponse), &deployResponse); jsonErr == nil {
					if deployResponse.ContainerID != "" {
						if err := handler.HandleExistingContainer(a, &deployResponse); err != nil {
							if strings.Contains(err.Error(), "cancelled by user") {
								return fmt.Errorf("deployment cancelled by user")
							}
							return fmt.Errorf("failed to handle existing container: %w", err)
						}
						// Reset the reader for retry
						imageReader.Seek(0, 0)
						continue // Retry deployment
					}
				}
			}
			return fmt.Errorf("deployment failed: %w", err)
		}

		// Parse the final response
		if resp != nil {
			var deployResp common.DeployResponse
			if err := json.Unmarshal(resp.Body, &deployResp); err != nil {
				return fmt.Errorf("failed to parse deployment response: %w", err)
			}
			finalResponse = &deployResp
		}
		break // Successful deployment
	}

	// Use the response data for waiting
	if finalResponse != nil {
		return waitForDeployment(finalResponse.Domain, finalResponse.ContainerID)
	}
	return fmt.Errorf("no response received from deployment")
}

func waitForDeployment(domain string, containerID string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	maxRetries := 20
	retryInterval := time.Second

	var shortContainerID string
	if containerID != "" {
		shortContainerID = containerID[:12]
	}

	log.Info("Waiting for deployment to be reachable",
		"domain", domain,
		"container_id", shortContainerID)

	for i := 0; i < maxRetries; i++ {
		resp, err := client.Get(domain)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				log.Info("Deployment successful",
					"domain", domain,
					"container_id", shortContainerID)
				return nil
			}
			body, _ := io.ReadAll(resp.Body)
			if strings.Contains(string(body), "failed to create container:") {
				return fmt.Errorf("deployment failed: %s", string(body))
			}
		}
		log.Warn("Deployment not ready yet, retrying",
			"attempt", fmt.Sprintf("%d/%d", i+1, maxRetries))
		time.Sleep(retryInterval)
	}

	return fmt.Errorf("deployment not ready after %d attempts", maxRetries)
}

func validateInputs(imageName, port, targetDomain string) error {
	if err := handler.ValidateImageName(imageName); err != nil {
		return err
	}

	handler.EnsureImageTag(&imageName)

	if port == "" {
		return fmt.Errorf("port is required")
	}

	return handler.ValidateTargetDomain(targetDomain)
}

func handleDeployError(a *cli.App, err error, imageName, port, targetDomain string, reader io.Reader) error {
	var deployErr *common.DeploymentError
	if errors.As(err, &deployErr) {
		var deployResponse common.DeployResponse
		if jsonErr := json.Unmarshal([]byte(deployErr.RawResponse), &deployResponse); jsonErr == nil {
			if deployErr.StatusCode == http.StatusConflict && deployResponse.ContainerID != "" {
				if err := handler.HandleExistingContainer(a, &deployResponse); err != nil {
					if strings.Contains(err.Error(), "cancelled by user") {
						log.Warn("Deployment cancelled by user")
						return nil
					}
					log.Error("Failed to handle existing container", "error", err)
					return err
				}
				// Retry deployment
				return deployImage(a, reader, imageName, port, targetDomain)
			}
		}
	}
	return err
}
