// endpoint_deploy.go
package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	authToken "github.com/bnema/gordon/internal/cli/auth"
	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/store"
	"github.com/charmbracelet/log"
	"github.com/labstack/echo/v4"
)

// PostDeploy handles the container deployment request
func PostDeploy(c echo.Context, a *server.App) error {
	chunkMetadataStr := c.Request().Header.Get("X-Chunk-Metadata")

	return handleChunkedDeploy(c, a, chunkMetadataStr)
}

func handleChunkedDeploy(c echo.Context, a *server.App, metadataStr string) error {
    // First, get the target domain from headers
    targetDomain := c.Request().Header.Get("X-Target-Domain")
    if targetDomain == "" {
        return sendJSONResponse(c, http.StatusBadRequest, DeployResponse{
            Success: false,
            Message: "Target domain is required",
        })
    }

    // Extract container name from the target domain
    cleanDomain := strings.TrimPrefix(strings.TrimPrefix(targetDomain, "https://"), "http://")
    containerName := strings.Split(cleanDomain, ".")[0]

    log.Info("Checking for existing container", "name", containerName)

    // Check if container already exists
    containerID := docker.GetContainerIDByName(containerName)
    if containerID != "" {
        log.Warn("Container already exists",
            "name", containerName,
            "id", containerID)
        return sendJSONResponse(c, http.StatusConflict, DeployResponse{
            Success:       false,
            Message:       fmt.Sprintf("Container with name '%s' already exists", containerName),
            ContainerID:   containerID,
            ContainerName: containerName,
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

    // Parse chunk metadata
    var metadata ChunkMetadata
    if err := json.Unmarshal([]byte(metadataStr), &metadata); err != nil {
        return sendJSONResponse(c, http.StatusBadRequest, DeployResponse{
            Success: false,
            Message: "Invalid chunk metadata",
        })
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

    chunkStore.chunks[metadata.TransferID][metadata.ChunkNumber] = chunkData
    chunkStore.mu.Unlock()

    // If this is the last chunk, process the complete transfer
    if isTransferComplete(metadata.TransferID) {
        return processCompleteDeployTransfer(c, a, metadata.TransferID)
    }

    // Include domain information in intermediate chunk responses
    return sendJSONResponse(c, http.StatusOK, DeployResponse{
        Success: true,
        Message: fmt.Sprintf("Chunk %d received successfully", metadata.ChunkNumber),
        Domain:  payload.TargetDomain,
    })
}

func processCompleteDeployTransfer(c echo.Context, a *server.App, transferID string) error {
    log.Info("Starting complete deploy transfer processing", "transferID", transferID)

    // Get deployment-specific headers
    port := normalizePort(c.Request().Header.Get("X-Ports"))
    targetDomain := normalizeTargetDomain(c.Request().Header.Get("X-Target-Domain"))
    imageName := c.Request().Header.Get("X-Image-Name")

    // Extract container name and check again (safety check)
    cleanDomain := strings.TrimPrefix(strings.TrimPrefix(targetDomain, "https://"), "http://")
    containerName := strings.Split(cleanDomain, ".")[0]

    containerID := docker.GetContainerIDByName(containerName)
    if containerID != "" {
        log.Warn("Container already exists during final processing",
            "name", containerName,
            "id", containerID)
        return sendJSONResponse(c, http.StatusConflict, DeployResponse{
            Success:       false,
            Message:       fmt.Sprintf("Container with name '%s' already exists", containerName),
            ContainerID:   containerID,
            ContainerName: containerName,
        })
    }

    // Combine chunks
    chunkStore.mu.Lock()
    metadata := chunkStore.metadata[transferID]
    chunks := chunkStore.chunks[transferID]
    chunkStore.mu.Unlock()

    if metadata == nil {
        return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
            Success: false,
            Message: "Transfer metadata not found",
        })
    }

    combinedData := bytes.NewBuffer(nil)
    for i := 0; i < metadata.TotalChunks; i++ {
        chunk, exists := chunks[i]
        if !exists {
            return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
                Success: false,
                Message: fmt.Sprintf("Missing chunk %d", i),
            })
        }
        combinedData.Write(chunk)
    }

    // Clean up the transfer data
    defer cleanupTransfer(transferID)

    // Save and import the image
    imageFileName := sanitizeImageFileName(imageName)
    imagePath, err := store.SaveImageToStorage(&a.Config, imageFileName, combinedData)
    if err != nil {
        return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
            Success: false,
            Message: fmt.Sprintf("Failed to save image: %v", err),
        })
    }

    imageID, err := docker.ImportImageToEngine(imagePath)
    if err != nil {
        store.RemoveFromStorage(imagePath)
        return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
            Success: false,
            Message: fmt.Sprintf("Failed to import image: %v", err),
        })
    }

    if err := store.RemoveFromStorage(imagePath); err != nil {
        log.Warn("Failed to remove temporary image file", "error", err)
    }

    // Create and start the container
    containerID, containerName, err = createAndStartContainer(a, &common.DeployPayload{
        Port:         port,
        TargetDomain: targetDomain,
        ImageName:    imageName,
        ImageID:      imageID,
    })

    if err != nil {
        if strings.Contains(err.Error(), "is already in use") {
            existingContainerID := extractContainerID(err.Error())
            existingContainerName, _ := docker.GetContainerName(existingContainerID)
            existingContainerName = strings.TrimPrefix(existingContainerName, "/")

            return sendJSONResponse(c, http.StatusConflict, DeployResponse{
                Success:       false,
                Message:       "A container for this deploy already exists.",
                ContainerID:   existingContainerID,
                ContainerName: existingContainerName,
            })
        }
        return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
            Success: false,
            Message: fmt.Sprintf("Failed to create or start container: %v", err),
        })
    }

    // Send final success response with all information
    return sendJSONResponse(c, http.StatusOK, DeployResponse{
        Success:       true,
        Message:       "Deployment successful",
        Domain:        targetDomain,
        ContainerID:   containerID,
        ContainerName: strings.TrimPrefix(containerName, "/"),
    })
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
