// gordon/internal/cli/cmd/deploy.go

package cmd

import (
	"bytes"
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
	"github.com/bnema/gordon/pkg/docker"
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

			reader, actualSize, err := exportDockerImage(imageName)
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

func exportDockerImage(imageName string) (io.ReadCloser, int64, error) {
	exists, err := docker.CheckIfImageExists(imageName)
	if err != nil {
		return nil, 0, fmt.Errorf("error checking image existence: %w", err)
	}

	var imageID string
	if exists {
		imageID = imageName
	} else {
		imageID, err = docker.GetImageIDByName(imageName)
		if err != nil {
			return nil, 0, fmt.Errorf("error searching for image by name: %w", err)
		}
	}

	actualSize, err := docker.GetImageSizeFromReader(imageID)
	if err != nil {
		return nil, 0, fmt.Errorf("error retrieving image size: %w", err)
	}

	reader, err := docker.ExportImageFromEngine(imageID)
	if err != nil {
		return nil, 0, fmt.Errorf("error exporting image: %w", err)
	}

	return reader, actualSize, nil
}

func deployImage(a *cli.App, reader io.Reader, imageName, port, targetDomain string) error {
	// Convert reader to bytes for reuse
	imageData, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("failed to read image data: %w", err)
	}

	log.Info("Attempting to deploy...",
		"image", imageName,
		"port", port,
		"target", targetDomain,
	)

	for {
		// Create new reader from bytes for each attempt
		reqPayload := common.RequestPayload{
			Type: "deploy",
			Payload: common.DeployPayload{
				Port:         port,
				TargetDomain: targetDomain,
				ImageName:    imageName,
				Data:         io.NopCloser(bytes.NewReader(imageData)),
			},
		}

		resp, err := handler.SendHTTPRequest(a, &reqPayload, "POST", "/deploy")
		if err != nil {
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
						log.Info("Retrying deployment...")
						continue // Retry deployment
					}
				}
			}
			return err
		}

		var deployResponse common.DeployResponse
		if err := json.Unmarshal(resp.Body, &deployResponse); err != nil {
			log.Error("Error parsing response", "error", err, "response", string(resp.Body))
			return nil
		}

		if !deployResponse.Success {
			log.Error("Deployment failed",
				"message", deployResponse.Message,
				"container_id", deployResponse.ContainerID,
			)
			return nil
		}

		return waitForDeployment(deployResponse.Domain, deployResponse.ContainerID)
	}
}

func waitForDeployment(domain string, containerID string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	maxRetries := 20
	retryInterval := time.Second
	// shorten the container id to 12 characters
	shortContainerID := containerID[:12]

	log.Info("Waiting for deployment to be reachable")
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
			// Check for error messages in the response body
			body, _ := io.ReadAll(resp.Body)
			if strings.Contains(string(body), "failed to create container:") {
				return fmt.Errorf("deployment failed: %s", string(body))
			}
		}
		log.Warn("Deployment not ready yet, retrying", "attempt", fmt.Sprintf("%d/%d", i+1, maxRetries))
		time.Sleep(retryInterval)
	}

	return fmt.Errorf("deployment not ready after %d attempts, giving up", maxRetries)
}
