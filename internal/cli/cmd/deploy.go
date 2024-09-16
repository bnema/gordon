// gordon/internal/cli/cmd/deploy.go

package cmd

import (
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
	"github.com/bnema/gordon/pkg/docker"
	"github.com/charmbracelet/log"
	"github.com/spf13/cobra"
)

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

			log.Info("Image exported successfully", "image", imageName, "size", actualSize)

			if err := deployImage(a, reader, imageName, port, targetDomain); err != nil {
				if deployErr, ok := err.(*common.DeploymentError); ok {
					log.Error("Deployment failed",
						"status_code", deployErr.StatusCode,
						"message", deployErr.Message,
					)
				} else {
					log.Error("Deployment failed", "error", err)
				}
				return
			}

			log.Info("Deployment successful!")
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
	reqPayload := common.RequestPayload{
		Type: "deploy",
		Payload: common.DeployPayload{
			Port:         port,
			TargetDomain: targetDomain,
			ImageName:    imageName,
			Data:         io.NopCloser(reader),
		},
	}

	resp, err := handler.SendHTTPRequest(a, &reqPayload, "POST", "/deploy")
	if err != nil {
		return &common.DeploymentError{
			StatusCode: 0,
			Message:    fmt.Sprintf("error sending HTTP request: %v", err),
		}
	}

	var deployResponse common.DeployResponse
	if err := json.Unmarshal(resp.Body, &deployResponse); err != nil {
		return &common.DeploymentError{
			StatusCode:  resp.StatusCode,
			Message:     fmt.Sprintf("error parsing response: %v", err),
			RawResponse: string(resp.Body),
		}
	}

	if !deployResponse.Success {
		return &common.DeploymentError{
			StatusCode:  resp.StatusCode,
			Message:     deployResponse.Message,
			RawResponse: string(resp.Body),
		}
	}

	log.Info("Application deployed", "domain", deployResponse.Domain)
	return waitForDeployment(deployResponse.Domain)
}

func waitForDeployment(domain string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	maxRetries := 20
	retryInterval := time.Second

	log.Info("Waiting for deployment to be reachable")
	for i := 0; i < maxRetries; i++ {
		resp, err := client.Get(domain)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			// Check for error messages in the response body
			body, _ := io.ReadAll(resp.Body)
			if strings.Contains(string(body), "failed to create container:") {
				return fmt.Errorf("deployment failed: %s", string(body))
			}
		}
		log.Warn("Deployment not ready yet, retrying", "attempt", fmt.Sprintf("%d/20", i+1))
		time.Sleep(retryInterval)
	}

	return fmt.Errorf("deployment not ready after %d attempts, giving up", maxRetries)
}
