// gordon/internal/cli/cmd/deploy.go

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
	log.SetTimeFormat("15:04:05")
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

			if err := validateDeployInputs(imageName, port, targetDomain); err != nil {
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

	deployCmd.Flags().StringVarP(&port, "port", "p", "", "Container port for the proxy to route traffic to")
	deployCmd.Flags().StringVarP(&targetDomain, "target", "t", "", "Target domain for the proxy")

	return deployCmd
}

func deployImage(a *cli.App, reader io.Reader, imageName, port, targetDomain string) error {
	// Check authentication early
	if err := handler.CheckAndRefreshAuth(a); err != nil {
		log.Error("Authentication check failed", "error", err)
		return err
	}

	log.Info("Deploying image", "image", imageName)
	// Check for conflicts first
	resp, err := checkDeployConflict(a, targetDomain, port)
	if err != nil {
		return fmt.Errorf("conflict check failed: %w", err)
	}

	var shortID string
	if len(resp.ContainerID) >= 12 {
		shortID = resp.ContainerID[:12]
	} else {
		shortID = resp.ContainerID // Use full ID if less than 12 chars
	}

	// If there's a conflict, handle it before proceeding
	if !resp.Success && resp.ContainerID != "" {
		log.Warn("Container already exists",
			"name", resp.ContainerName,
			"id", shortID,
			"state", resp.State,
			"uptime", resp.RunningTime)

		if err := handler.HandleExistingContainer(a, resp); err != nil {
			if strings.Contains(err.Error(), "cancelled by user") {
				return fmt.Errorf("deployment cancelled by user")
			}
			return fmt.Errorf("failed to handle existing container: %w", err)
		}

	}

	var buf bytes.Buffer
	size, err := io.Copy(&buf, reader)
	if err != nil {
		return fmt.Errorf("failed to read image data: %w", err)
	}

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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	chunkResp, err := chunkedClient.SendFileAsChunks(ctx, "/deploy/chunked", headers, imageReader, size, imageName)
	if err != nil {
		return fmt.Errorf("failed to deploy image: %w", err)
	}

	if chunkResp == nil {
		return fmt.Errorf("received nil response from server")
	}

	var deployResp common.DeployResponse
	if err := json.Unmarshal(chunkResp.Body, &deployResp); err != nil {
		return fmt.Errorf("failed to parse deployment response: %w", err)
	}

	return waitForDeployment(deployResp.Domain, deployResp.ContainerID)
}

func waitForDeployment(domain string, containerID string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	maxRetries := 10
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

func validateDeployInputs(imageName, port, targetDomain string) error {
	if err := handler.ValidateImageName(imageName); err != nil {
		return err
	}

	if err := handler.EnsureImageTag(&imageName); err != nil {
		return fmt.Errorf("failed to ensure image tag: %w", err)
	}

	if port == "" {
		return fmt.Errorf("port is required")
	}

	return handler.ValidateTargetDomain(targetDomain)
}

func checkDeployConflict(a *cli.App, targetDomain string, port string) (*common.DeployResponse, error) {

	// Create request to check-conflict endpoint with both domain and port parameters
	reqUrl := fmt.Sprintf("%s/api/deploy/check-conflict?domain=%s&port=%s",
		a.Config.Http.BackendURL, targetDomain, port)

	req, err := http.NewRequest("GET", reqUrl, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create conflict check request: %w", err)
	}

	// Set auth header
	req.Header.Set("Authorization", "Bearer "+a.Config.General.Token)

	// Send request
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send conflict check request: %w", err)
	}
	defer resp.Body.Close()

	var conflictResp common.DeployResponse
	if err := json.NewDecoder(resp.Body).Decode(&conflictResp); err != nil {
		return nil, fmt.Errorf("failed to parse conflict check response: %w", err)
	}

	return &conflictResp, nil
}
