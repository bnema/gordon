// endpoint_deploy.go
package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	authToken "github.com/bnema/gordon/internal/cli/auth"
	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/charmbracelet/log"
	"github.com/labstack/echo/v4"
)

// PostDeploy handles the container deployment request
func PostDeploy(c echo.Context, a *server.App) error {
	payload, err := validateAndPrepareDeployPayload(c)
	if err != nil {
		// Debug
		log.Error("Failed to validate and prepare payload", "error", err)
		return sendJSONResponse(c, http.StatusBadRequest, DeployResponse{
			Success: false,
			Message: fmt.Sprintf("Invalid payload: %v", err),
		})
	}

	_, err = saveAndImportDeployImage(c, a, payload)
	if err != nil {
		// Debug
		log.Error("Failed to save and import image", "error", err)
		return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to save or import image: %v", err),
		})
	}

	containerID, containerName, err := createAndStartContainer(a, payload)
	if err != nil {
		if strings.Contains(err.Error(), "is already in use") {
			log.Warn("Container already exists", "error", err)
			existingContainerID := extractContainerID(err.Error())
			existingContainerName, _ := docker.GetContainerName(existingContainerID)
			// Remove the / from the container name
			existingContainerName = strings.TrimLeft(existingContainerName, "/")

			if existingContainerID == "" || existingContainerName == "" {
				log.Error("Failed to extract container ID or Name from error message")
				return sendJSONResponse(c, http.StatusConflict, DeployResponse{
					Success: false,
					Message: "A container for this deploy already exists but could not be identified.",
				})
			}

			return sendJSONResponse(c, http.StatusConflict, DeployResponse{
				Success:       false,
				Message:       "A container for this deploy already exists.",
				ContainerID:   existingContainerID,
				ContainerName: existingContainerName,
			})
		}
		log.Error("Failed to create or start container", "error", err)
		return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to create or start container: %v", err),
		})
	}
	response := DeployResponse{
		Success:       true,
		Message:       "Deployment successful",
		Domain:        payload.TargetDomain,
		ContainerID:   containerID,
		ContainerName: containerName,
	}

	// Debug: Verify containerID in response
	if response.ContainerID != containerID {
		log.Error("Container ID mismatch", "responseID", response.ContainerID, "actualID", containerID)
	} else {
		log.Info("Container ID successfully stored in response", "containerID", containerID)
	}

	return sendJSONResponse(c, http.StatusOK, response)
}

// PostDeployChunked handles the chunked container deployment request
func PostDeployChunked(c echo.Context, a *server.App) error {
	return handleChunkedDeploy(c, a, c.Request().Header.Get("X-Chunk-Metadata"))
}

func handleChunkedDeploy(c echo.Context, a *server.App, metadataStr string) error {
	var metadata ChunkMetadata
	if err := json.Unmarshal([]byte(metadataStr), &metadata); err != nil {
		return sendJSONResponse(c, http.StatusBadRequest, DeployResponse{
			Success: false,
			Message: "Invalid chunk metadata",
		})
	}

	payload, err := validateAndPrepareDeployPayload(c)
	if err != nil {
		log.Error("Failed to validate and prepare payload", "error", err)
		return sendJSONResponse(c, http.StatusBadRequest, DeployResponse{
			Success: false,
			Message: fmt.Sprintf("Invalid payload: %v", err),
		})
	}

	// Authenticate request
	auth := c.Request().Header.Get("Authorization")
	if auth == "" {
		return sendJSONResponse(c, http.StatusUnauthorized, DeployResponse{
			Success: false,
			Message: "Authentication required",
		})
	}

	token := strings.TrimPrefix(auth, "Bearer ")
	if !authToken.IsValidGitHubToken(token) {
		return sendJSONResponse(c, http.StatusUnauthorized, DeployResponse{
			Success: false,
			Message: "Invalid or expired token",
		})
	}

	if metadata.TotalChunks == 1 {
		chunkData, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
				Success: false,
				Message: "Failed to read chunk data",
			})
		}

		// Store the data directly
		chunkStore.mu.Lock()
		chunkStore.chunks[metadata.TransferID] = make(map[int][]byte)
		chunkStore.chunks[metadata.TransferID][0] = chunkData
		chunkStore.metadata[metadata.TransferID] = &metadata
		chunkStore.mu.Unlock()

		// Process the complete transfer immediately
		return processCompleteChunkedDeployTansfert(c, a, metadata.TransferID)
	}

	// Store chunk data
	chunkStore.mu.Lock()
	if chunkStore.chunks[metadata.TransferID] == nil {
		chunkStore.chunks[metadata.TransferID] = make(map[int][]byte)
		chunkStore.metadata[metadata.TransferID] = &metadata
	}

	chunkData, err := io.ReadAll(c.Request().Body)
	if err != nil {
		chunkStore.mu.Unlock()
		return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
			Success: false,
			Message: "Failed to read chunk data",
		})
	}

	// Store the complete chunk
	chunkStore.chunks[metadata.TransferID][metadata.ChunkNumber] = chunkData
	chunkStore.mu.Unlock()

	// If this is the last chunk, process the complete transfer
	if isTransferComplete(metadata.TransferID) {
		return processCompleteChunkedDeployTansfert(c, a, metadata.TransferID)
	}

	// Include domain information in intermediate chunk responses
	return sendJSONResponse(c, http.StatusOK, DeployResponse{
		Success: true,
		Message: fmt.Sprintf("Chunk %d/%d received", metadata.ChunkNumber+1, metadata.TotalChunks),
		Domain:  payload.TargetDomain,
	})
}

func processCompleteChunkedDeployTansfert(c echo.Context, a *server.App, transferID string) error {
	log.Info("Starting complete deploy transfer processing",
		"transferID", transferID)

	// Get transfer data
	metadata, chunks, err := getTransferData(transferID)
	if err != nil {
		return sendErrorResponse(c, "Failed to get transfer data", err)
	}

	// Create temporary directory for processing
	tmpDir, err := os.MkdirTemp("", "docker-deploy-*")
	if err != nil {
		return sendErrorResponse(c, "Failed to create temp directory", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpFile, err := os.Create(filepath.Join(tmpDir, "image.tar"))
	if err != nil {
		return sendErrorResponse(c, "Failed to create temp file", err)
	}
	defer tmpFile.Close()

	// Write chunks to file
	if err := writeChunksToFile(tmpFile, chunks, metadata.TotalChunks); err != nil {
		return sendErrorResponse(c, "Failed to write chunks", err)
	}

	// Get deployment-specific headers
	port := normalizePort(c.Request().Header.Get("X-Ports"))
	targetDomain := normalizeTargetDomain(c.Request().Header.Get("X-Target-Domain"))
	imageName := c.Request().Header.Get("X-Image-Name")

	if port == "" || targetDomain == "" || imageName == "" {
		return sendErrorResponse(c, "Missing required deployment parameters",
			fmt.Errorf("port: %s, domain: %s, image: %s", port, targetDomain, imageName))
	}

	// Extract container name from domain and perform safety checks
	cleanDomain := strings.TrimPrefix(strings.TrimPrefix(targetDomain, "https://"), "http://")
	containerName := strings.Split(cleanDomain, ".")[0]

	if containerName == "" {
		return sendErrorResponse(c, "Invalid target domain",
			fmt.Errorf("couldn't extract container name from domain: %s", targetDomain))
	}

	// Check for existing container
	existingContainerID := docker.GetContainerIDByName(containerName)
	if existingContainerID != "" {
		log.Warn("Container already exists",
			"name", containerName,
			"id", existingContainerID)
		return sendJSONResponse(c, http.StatusConflict, DeployResponse{
			Success:       false,
			Message:       fmt.Sprintf("Container with name '%s' already exists", containerName),
			ContainerID:   existingContainerID,
			ContainerName: containerName,
		})
	}

	// Import the image
	log.Info("Importing image from file",
		"path", tmpFile.Name(),
		"imageName", imageName)

	imageID, err := docker.ImportImageToEngine(tmpFile.Name())
	if err != nil {
		if strings.Contains(err.Error(), "platform compatibility check failed") {
			log.Error("Platform compatibility check failed",
				"error", err,
				"imageName", imageName)
			return c.JSON(http.StatusBadRequest, &common.DeploymentError{
				StatusCode:  http.StatusBadRequest,
				Message:     "Platform compatibility error",
				RawResponse: err.Error(),
			})
		}
		return sendErrorResponse(c, "Failed to import image", err)
	}

	// Prepare deployment payload
	deployPayload := &common.DeployPayload{
		Port:         port,
		TargetDomain: targetDomain,
		ImageName:    imageName,
		ImageID:      imageID,
	}

	// Create and start the container
	containerID, containerName, err := createAndStartContainer(a, deployPayload)
	if err != nil {
		if strings.Contains(err.Error(), "is already in use") {
			existingContainerID := extractContainerID(err.Error())
			existingContainerName, _ := docker.GetContainerName(existingContainerID)
			existingContainerName = strings.TrimPrefix(existingContainerName, "/")

			return c.JSON(http.StatusBadRequest, &common.DeploymentError{
				StatusCode:  http.StatusBadRequest,
				Message:     fmt.Sprintf("Container with name '%s' already exists", existingContainerName),
				RawResponse: err.Error(),
			})
		}

		return sendErrorResponse(c, "Failed to create or start container", err)
	}

	// Cleanup transfer data
	cleanupTransfer(transferID)

	log.Info("Deployment completed successfully",
		"containerID", containerID,
		"containerName", containerName,
		"domain", targetDomain)

	return sendJSONResponse(c, http.StatusOK, DeployResponse{
		Success:       true,
		Message:       "Deployment successful",
		Domain:        targetDomain,
		ContainerID:   containerID,
		ContainerName: strings.TrimPrefix(containerName, "/"),
	})
}

// func waitForDeployment(domain string, containerID string) error {
// 	client := &http.Client{Timeout: 10 * time.Second}
// 	maxRetries := 10
// 	retryInterval := time.Second

// 	var shortContainerID string
// 	if containerID != "" {
// 		shortContainerID = containerID[:12]
// 	}

// 	log.Info("Waiting for deployment to be reachable",
// 		"domain", domain,
// 		"container_id", shortContainerID)

// 	for i := 0; i < maxRetries; i++ {
// 		resp, err := client.Get(domain)
// 		if err == nil {
// 			defer resp.Body.Close()
// 			if resp.StatusCode == http.StatusOK {
// 				log.Info("Deployment successful",
// 					"domain", domain,
// 					"container_id", shortContainerID)
// 				return nil
// 			}
// 			body, _ := io.ReadAll(resp.Body)
// 			if strings.Contains(string(body), "failed to create container:") {
// 				return fmt.Errorf("deployment failed: %s", string(body))
// 			}
// 		}
// 		log.Warn("Deployment not ready yet, retrying",
// 			"attempt", fmt.Sprintf("%d/%d", i+1, maxRetries))
// 		time.Sleep(retryInterval)
// 	}

// 	return fmt.Errorf("deployment not ready after %d attempts", maxRetries)
// }
