// endpoint_push.go
package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/store"
	"github.com/charmbracelet/log"
	"github.com/labstack/echo/v4"
)

// PostPush handles the image push request
func PostPush(c echo.Context, a *server.App) error {

	requestID := c.Response().Header().Get(echo.HeaderXRequestID)
	log.Debug("PostPush started",
		"requestID", requestID,
		"path", c.Request().URL.Path)

	chunkMetadataStr := c.Request().Header.Get("X-Chunk-Metadata")
	if chunkMetadataStr != "" {
		return handleChunkedPush(c, a, chunkMetadataStr)
	}

	return handleRegularPush(c, a)
}
func handleChunkedPush(c echo.Context, a *server.App, chunkMetadataStr string) error {
	var metadata common.ChunkMetadata
	if err := json.Unmarshal([]byte(chunkMetadataStr), &metadata); err != nil {
		return sendJSONResponse(c, http.StatusBadRequest, PushResponse{
			Success: false,
			Message: "Invalid chunk metadata",
		})
	}

	// Create temporary file for the transfer if it doesn't exist
	transferPath := fmt.Sprintf("data/transfers/%s.tar", metadata.TransferID)
	if metadata.ChunkNumber == 0 {
		if err := os.MkdirAll("data/transfers", 0755); err != nil {
			return sendJSONResponse(c, http.StatusInternalServerError, PushResponse{
				Success: false,
				Message: fmt.Sprintf("Failed to create transfers directory: %v", err),
			})
		}
	}

	// Open file in append mode
	f, err := os.OpenFile(transferPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return sendJSONResponse(c, http.StatusInternalServerError, PushResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to open transfer file: %v", err),
		})
	}
	defer f.Close()

	// Copy chunk data to file
	if _, err := io.Copy(f, c.Request().Body); err != nil {
		return sendJSONResponse(c, http.StatusInternalServerError, PushResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to write chunk: %v", err),
		})
	}

	// If this is the last chunk, process the complete image
	if metadata.ChunkNumber == metadata.TotalChunks-1 {
		// Close the file before importing
		f.Close()

		imageID, err := docker.ImportImageToEngine(transferPath)
		if err != nil {
			os.Remove(transferPath)
			return sendJSONResponse(c, http.StatusInternalServerError, PushResponse{
				Success: false,
				Message: fmt.Sprintf("Failed to import image: %v", err),
			})
		}

		// Clean up
		os.Remove(transferPath)

		shortID := generateShortID(imageID)
		storeIDMapping(shortID, imageID)
		createContainerURL := generateCreateContainerURL(a, shortID)

		return sendJSONResponse(c, http.StatusOK, PushResponse{
			Success:            true,
			Message:            "Image pushed and imported successfully",
			CreateContainerURL: createContainerURL,
			ImageID:            imageID,
		})
	}

	// For intermediate chunks, just acknowledge receipt
	return sendJSONResponse(c, http.StatusOK, PushResponse{
		Success: true,
		Message: fmt.Sprintf("Chunk %d/%d received", metadata.ChunkNumber+1, metadata.TotalChunks),
	})
}

func handleRegularPush(c echo.Context, a *server.App) error {
	payload, err := validateAndPreparePayload(c)
	if err != nil {
		return sendJSONResponse(c, http.StatusBadRequest, PushResponse{
			Success: false,
			Message: err.Error(),
		})
	}

	err = saveAndImportImage(c, a, payload)
	if err != nil {
		return sendJSONResponse(c, http.StatusInternalServerError, PushResponse{
			Success: false,
			Message: err.Error(),
		})
	}

	shortID := generateShortID(payload.ImageID)
	storeIDMapping(shortID, payload.ImageID)
	createContainerURL := generateCreateContainerURL(a, shortID)

	return sendJSONResponse(c, http.StatusOK, PushResponse{
		Success:            true,
		Message:            "Image pushed and imported successfully",
		CreateContainerURL: createContainerURL,
	})
}

func processCompletePushTransfer(c echo.Context, a *server.App, transferID string) error {
	chunkStore.mu.Lock()
	metadata := chunkStore.metadata[transferID]
	chunks := chunkStore.chunks[transferID]
	chunkStore.mu.Unlock()

	if metadata == nil {
		return sendJSONResponse(c, http.StatusBadRequest, PushResponse{
			Success: false,
			Message: "Transfer metadata not found",
		})
	}

	// Combine chunks
	combinedData := bytes.NewBuffer(nil)
	for i := 0; i < metadata.TotalChunks; i++ {
		chunk, exists := chunks[i]
		if !exists {
			return sendJSONResponse(c, http.StatusBadRequest, PushResponse{
				Success: false,
				Message: fmt.Sprintf("Missing chunk %d", i),
			})
		}
		combinedData.Write(chunk)
	}

	// Clean up the transfer data early
	defer cleanupTransfer(transferID)

	// Process the image
	imageFileName := sanitizeImageFileName(metadata.ImageName)
	imagePath, err := store.SaveImageToStorage(&a.Config, imageFileName, combinedData)
	if err != nil {
		return sendJSONResponse(c, http.StatusInternalServerError, PushResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to save image: %v", err),
		})
	}

	imageID, err := docker.ImportImageToEngine(imagePath)
	if err != nil {
		store.RemoveFromStorage(imagePath)
		return sendJSONResponse(c, http.StatusInternalServerError, PushResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to import image: %v", err),
		})
	}

	if err := store.RemoveFromStorage(imagePath); err != nil {
		log.Warn("Failed to remove temporary image file", "error", err)
	}

	shortID := generateShortID(imageID)
	storeIDMapping(shortID, imageID)
	createContainerURL := generateCreateContainerURL(a, shortID)

	return sendJSONResponse(c, http.StatusOK, PushResponse{
		Success:            true,
		Message:            "Image pushed and imported successfully",
		CreateContainerURL: createContainerURL,
		ImageID:            imageID,
	})
}
