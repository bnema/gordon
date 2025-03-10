// endpoint_deploy.go
package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	authToken "github.com/bnema/gordon/internal/cli/auth"
	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/proxy"
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
			Success:    false,
			Message:    fmt.Sprintf("Invalid payload: %v", err),
			StatusCode: http.StatusBadRequest,
		})
	}

	_, err = saveAndImportImage(c, a, payload)
	if err != nil {
		// Debug
		log.Error("Failed to save and import image", "error", err)
		return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
			Success:    false,
			Message:    fmt.Sprintf("Failed to save or import image: %v", err),
			StatusCode: http.StatusInternalServerError,
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
					Success:    false,
					Message:    "A container for this deploy already exists but could not be identified.",
					StatusCode: http.StatusConflict,
				})
			}

			return sendJSONResponse(c, http.StatusConflict, DeployResponse{
				Success:       false,
				Message:       "A container for this deploy already exists.",
				ContainerID:   existingContainerID,
				ContainerName: existingContainerName,
				StatusCode:    http.StatusConflict,
			})
		}
		log.Error("Failed to create or start container", "error", err)
		return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
			Success:    false,
			Message:    fmt.Sprintf("Failed to create or start container: %v", err),
			StatusCode: http.StatusInternalServerError,
		})
	}

	// After successfully deploying a container, add a proxy route
	containerIP := GetContainerIP(a, containerID, containerName)
	err = AddProxyRoute(a, containerID, containerIP, payload.Port, payload.TargetDomain)
	if err != nil {
		log.Error("Failed to add proxy route", "error", err)
		return sendDeployErrorResponse(c, "Failed to add proxy route", err)
	}

	// Arrived here means deployment was successful
	return sendJSONResponse(c, http.StatusOK, DeployResponse{
		Success:       true,
		Message:       "Deployment successful",
		Domain:        payload.TargetDomain,
		ContainerID:   containerID,
		ContainerName: containerName,
	})
}

// PostDeployChunked handles the chunked container deployment request
func PostDeployChunked(c echo.Context, a *server.App) error {
	return handleChunkedDeploy(c, a, c.Request().Header.Get("X-Chunk-Metadata"))
}

func handleChunkedDeploy(c echo.Context, a *server.App, metadataStr string) error {
	var metadata ChunkMetadata
	if err := json.Unmarshal([]byte(metadataStr), &metadata); err != nil {
		return sendJSONResponse(c, http.StatusBadRequest, DeployResponse{
			Success:    false,
			Message:    "Invalid chunk metadata",
			StatusCode: http.StatusBadRequest,
		})
	}

	payload, err := validateAndPrepareDeployPayload(c)
	if err != nil {
		log.Error("Failed to validate and prepare payload", "error", err)
		return sendJSONResponse(c, http.StatusBadRequest, DeployResponse{
			Success:    false,
			Message:    fmt.Sprintf("Invalid payload: %v", err),
			StatusCode: http.StatusBadRequest,
		})
	}

	// Auth verification steps

	auth := c.Request().Header.Get("Authorization")
	if auth == "" {
		return sendJSONResponse(c, http.StatusUnauthorized, DeployResponse{
			Success:    false,
			Message:    "Authentication required",
			StatusCode: http.StatusUnauthorized,
		})
	}

	// Validate the token through GitHub API
	token := strings.TrimPrefix(auth, "Bearer ")
	if !authToken.IsValidGitHubToken(token) {
		return sendJSONResponse(c, http.StatusUnauthorized, DeployResponse{
			Success:    false,
			Message:    "Invalid or expired token",
			StatusCode: http.StatusUnauthorized,
		})
	}

	// TODO: Here we should probably check that the github user is the same as the one in DB

	if metadata.TotalChunks == 1 {
		chunkData, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
				Success:    false,
				Message:    "Failed to read chunk data",
				StatusCode: http.StatusInternalServerError,
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
			Success:    false,
			Message:    "Failed to read chunk data",
			StatusCode: http.StatusInternalServerError,
		})
	}

	//  Chunks validation steps
	//
	if len(chunkData) == 0 {
		log.Error("Received empty chunk")
		return sendJSONResponse(c, http.StatusBadRequest, DeployResponse{
			Success:    false,
			Message:    "Received empty chunk",
			StatusCode: http.StatusBadRequest,
		})
	}

	// Verify chunk size matches metadata
	if int64(len(chunkData)) != metadata.ChunkSize {
		log.Error("Chunk size mismatch",
			"expected", metadata.ChunkSize,
			"received", len(chunkData))
		return sendJSONResponse(c, http.StatusBadRequest, DeployResponse{
			Success: false,
			Message: fmt.Sprintf("Chunk size mismatch: expected %d, got %d",
				metadata.ChunkSize, len(chunkData)),
			StatusCode: http.StatusBadRequest,
		})
	}

	// Store the complete chunk into our store
	chunkStore.chunks[metadata.TransferID][metadata.ChunkNumber] = chunkData
	chunkStore.mu.Unlock()

	// If this is the last chunk, process the complete transfer
	if isTransferComplete(metadata.TransferID) {
		return processCompleteChunkedDeployTansfert(c, a, metadata.TransferID)
	}

	// Include domain information in intermediate chunk responses
	return sendJSONResponse(c, http.StatusOK, DeployResponse{
		Success:    true,
		Message:    fmt.Sprintf("Chunk %d/%d received", metadata.ChunkNumber+1, metadata.TotalChunks),
		Domain:     payload.TargetDomain,
		StatusCode: http.StatusOK,
	})
}

func processCompleteChunkedDeployTansfert(c echo.Context, a *server.App, transferID string) error {
	log.Info("Starting complete deploy transfer processing",
		"transferID", transferID)

	// Get transfer data
	metadata, chunks, err := getTransferData(transferID)
	if err != nil {
		return sendDeployErrorResponse(c, "Failed to get transfer data", err)
	}

	// Create temporary directory for processing
	tmpDir, err := os.MkdirTemp("", "docker-deploy-*")
	if err != nil {
		return sendDeployErrorResponse(c, "Failed to create temp directory", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpFile, err := os.Create(filepath.Join(tmpDir, "image.tar"))
	if err != nil {
		return sendDeployErrorResponse(c, "Failed to create temp file", err)
	}
	defer tmpFile.Close()

	// Write chunks to file
	if err := writeChunksToFile(tmpFile, chunks, metadata.TotalChunks); err != nil {
		return sendDeployErrorResponse(c, "Failed to write chunks", err)
	}

	// Get deployment-specific headers
	port := normalizePort(c.Request().Header.Get("X-Ports"))
	targetDomain := normalizeTargetDomain(c.Request().Header.Get("X-Target-Domain"))
	imageName := c.Request().Header.Get("X-Image-Name")

	if port == "" || targetDomain == "" || imageName == "" {
		return sendDeployErrorResponse(c, "Missing required deployment parameters",
			fmt.Errorf("port: %s, domain: %s, image: %s", port, targetDomain, imageName))
	}

	// Extract container name from domain and perform safety checks
	cleanDomain := strings.TrimPrefix(strings.TrimPrefix(targetDomain, "https://"), "http://")
	containerName := strings.Split(cleanDomain, ".")[0]

	if containerName == "" {
		return sendDeployErrorResponse(c, "Invalid target domain",
			fmt.Errorf("couldn't extract container name from domain: %s", targetDomain))
	}

	// Check for existing container
	existingContainerID := docker.GetContainerIDByName(containerName)
	if existingContainerID != "" {
		log.Warn("Container already exists",
			"name", containerName,
			"id", existingContainerID)
		return sendDeployErrorResponse(c, "Container already exists", nil)
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
			return sendDeployErrorResponse(c, "Platform compatibility check failed", err)
		}
		return sendDeployErrorResponse(c, "Failed to import image", err)
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

			return sendDeployErrorResponse(c, "Container already in use", err)
		}

		return sendDeployErrorResponse(c, "Failed to create or start container", err)
	}

	// Cleanup transfer data
	cleanupTransfer(transferID)

	log.Info("Deployment completed successfully",
		"containerID", containerID,
		"containerName", containerName,
		"domain", targetDomain)

	// After successfully deploying a container, add a proxy route
	containerIP := GetContainerIP(a, containerID, containerName)
	err = AddProxyRoute(a, containerID, containerIP, port, targetDomain)
	if err != nil {
		log.Error("Failed to add proxy route", "error", err)
		return sendDeployErrorResponse(c, "Failed to add proxy route", err)
	}

	return sendJSONResponse(c, http.StatusOK, DeployResponse{
		Success:       true,
		Message:       "Deployment successful",
		Domain:        targetDomain,
		ContainerID:   containerID,
		ContainerName: strings.TrimPrefix(containerName, "/"),
	})
}

func sendDeployErrorResponse(c echo.Context, message string, err error) error {
	// The error becomes the message
	errorMsg := fmt.Sprintf("%s: %s", message, err.Error())
	return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
		Success:    false,
		Message:    errorMsg,
		StatusCode: http.StatusInternalServerError,
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

// After successfully deploying a container, add a proxy route
func AddProxyRoute(a *server.App, containerID, containerIP, containerPort, targetDomain string) error {
	// Create a new proxy instance
	p, err := proxy.NewProxy(a)
	if err != nil {
		return fmt.Errorf("failed to create proxy: %w", err)
	}

	// Double check that we have a valid container IP - critical for recreated containers
	if containerIP == "" || containerIP == containerID || containerIP == "localhost" {
		log.Warn("Invalid container IP detected, attempting to get correct IP",
			"containerID", containerID,
			"invalid_ip", containerIP)

		// Try to get the container name for better logging
		containerName, err := docker.GetContainerName(containerID)
		if err != nil {
			containerName = containerID
		} else {
			containerName = strings.TrimPrefix(containerName, "/")
		}

		// Get the correct container IP with retry logic
		newContainerIP := GetContainerIP(a, containerID, containerName)
		if newContainerIP != containerIP && newContainerIP != containerName {
			log.Info("Updated container IP for proxy route",
				"containerID", containerID,
				"old_ip", containerIP,
				"new_ip", newContainerIP)
			containerIP = newContainerIP
		}
	}

	// Get container info to check labels
	containerInfo, err := docker.GetContainerInfo(containerID)
	if err != nil {
		log.Warn("Failed to get container info", "error", err)
	} else {
		// Check if the container has a gordon.proxy.port label that might override containerPort
		if portLabel, exists := containerInfo.Config.Labels["gordon.proxy.port"]; exists && portLabel != "" {
			if portLabel != containerPort {
				log.Info("Using container label port instead of provided port",
					"label_port", portLabel,
					"provided_port", containerPort)
				containerPort = portLabel
			}
		}
	}

	// Extract the protocol from the target domain
	protocol := "http"

	// Check if the target domain specifies HTTPS
	if strings.HasPrefix(strings.ToLower(targetDomain), "https://") {
		protocol = "https"
		log.Debug("Using HTTPS protocol for proxy route based on target domain",
			"domain", targetDomain,
			"protocol", protocol)
	} else {
		// If target domain doesn't specify protocol, check the container labels
		if err == nil { // Only check if we have containerInfo
			// Check if gordon.proxy.ssl label is set to true
			if sslValue, exists := containerInfo.Config.Labels["gordon.proxy.ssl"]; exists &&
				(sslValue == "true" || sslValue == "1" || sslValue == "yes") {
				protocol = "https"
				log.Debug("Using HTTPS protocol for proxy route based on container labels",
					"domain", targetDomain,
					"containerID", containerID,
					"label_value", sslValue,
					"protocol", protocol)
			}
		}
	}

	// Clean domain if it includes protocol
	cleanDomain := targetDomain
	if strings.Contains(cleanDomain, "://") {
		cleanDomain = strings.Split(cleanDomain, "://")[1]
	}

	// Log that we're adding the domain for Let's Encrypt
	log.Debug("Adding domain for Let's Encrypt verification",
		"domain", cleanDomain)

	// Get container name for better logging
	containerName, err := docker.GetContainerName(containerID)
	if err != nil {
		log.Warn("Unable to get container name", "containerID", containerID, "error", err)
	} else {
		containerName = strings.TrimPrefix(containerName, "/")
		log.Debug("Container name for proxy route", "name", containerName)
	}

	// Check the database for any existing routes with this domain, regardless of container ID
	// This handles the case where a container is recreated with the same name but a different ID
	tx, err := a.GetDB().Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	var existingRouteID string
	var existingContainerID string
	err = tx.QueryRow(`
		SELECT pr.id, pr.container_id 
		FROM proxy_route pr 
		JOIN domain d ON pr.domain_id = d.id 
		WHERE d.name = ?`, cleanDomain).Scan(&existingRouteID, &existingContainerID)

	if err == nil {
		// Domain exists in database, check if this is a recreated container
		log.Info("Found existing proxy route for domain",
			"domain", cleanDomain,
			"old_container_id", existingContainerID,
			"new_container_id", containerID)

		// Verify container IP one last time before updating the route
		containerNetworkInfo, err := docker.GetContainerInfo(containerID)
		if err == nil && containerNetworkInfo.NetworkSettings != nil {
			// Check the container's network settings for the configured network
			networkName := a.Config.ContainerEngine.Network
			if networkSettings, exists := containerNetworkInfo.NetworkSettings.Networks[networkName]; exists &&
				networkSettings.IPAddress != "" && networkSettings.IPAddress != containerIP {
				log.Info("Updating container IP before database update",
					"domain", cleanDomain,
					"old_ip", containerIP,
					"new_ip", networkSettings.IPAddress)
				containerIP = networkSettings.IPAddress
			}
		}

		// If the container IDs are different, this is likely a recreated container
		// Update the route directly in the database
		now := time.Now().Format(time.RFC3339)
		_, err = tx.Exec(`
			UPDATE proxy_route 
			SET container_id = ?, container_ip = ?, container_port = ?, updated_at = ? 
			WHERE id = ?`,
			containerID, containerIP, containerPort, now, existingRouteID)

		if err != nil {
			return fmt.Errorf("failed to update proxy route: %w", err)
		}

		log.Info("Updated proxy route for recreated container",
			"domain", cleanDomain,
			"old_container_id", existingContainerID,
			"new_container_id", containerID,
			"container_ip", containerIP,
			"container_port", containerPort)

		// Commit transaction
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit transaction: %w", err)
		}

		// Verify the database update with a direct query
		var updatedContainerIP string
		err = a.GetDB().QueryRow(`
			SELECT container_ip 
			FROM proxy_route 
			WHERE id = ?`, existingRouteID).Scan(&updatedContainerIP)

		if err == nil && updatedContainerIP != containerIP {
			log.Warn("Database verification failed: container IP mismatch",
				"expected", containerIP,
				"actual", updatedContainerIP)
		} else {
			log.Debug("Database verification passed: container IP updated correctly",
				"container_ip", containerIP)
		}

		// Reload the routes
		if err := p.Reload(); err != nil {
			return fmt.Errorf("failed to reload routes: %w", err)
		}

		// Final verification: check that the route was loaded with the correct IP
		routes := p.GetRoutes()
		if route, exists := routes[cleanDomain]; exists {
			if route.ContainerIP != containerIP {
				log.Warn("Route reload verification failed: container IP mismatch",
					"domain", cleanDomain,
					"expected", containerIP,
					"actual", route.ContainerIP)
			} else {
				log.Debug("Route reload verification passed: container IP loaded correctly",
					"domain", cleanDomain,
					"container_ip", containerIP)
			}
		}

		return nil
	} else if err != sql.ErrNoRows {
		// A database error other than "not found"
		return fmt.Errorf("failed to query database for existing route: %w", err)
	}

	// Route doesn't exist or we had ErrNoRows, continue with normal AddRoute
	// Add the route - this will also add to the domain database for Let's Encrypt
	if err := p.AddRoute(cleanDomain, containerID, containerIP, containerPort, protocol, "/"); err != nil {
		return fmt.Errorf("failed to add proxy route: %w", err)
	}

	log.Debug("Added proxy route",
		"domain", targetDomain,
		"containerIP", containerIP,
		"containerPort", containerPort,
		"protocol", protocol,
	)
	return nil
}
