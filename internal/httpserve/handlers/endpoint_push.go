// endpoint_push.go
package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/charmbracelet/log"
	"github.com/labstack/echo/v4"
)

// PostPush handles the image push request
func PostPush(c echo.Context, a *server.App) error {
	requestID := c.Response().Header().Get(echo.HeaderXRequestID)
	log.Info("PostPush started",
		"requestID", requestID,
		"path", c.Request().URL.Path)
	payload, err := validateAndPreparePushPayload(c)
	if err != nil {
		return sendJSONResponse(c, http.StatusBadRequest, PushResponse{
			Success: false,
			Message: err.Error(),
		})
	}

	_, err = saveAndImportImage(c, a, payload)
	if err != nil {
		return sendJSONResponse(c, http.StatusInternalServerError, PushResponse{
			Success: false,
			Message: err.Error(),
		})
	}

	shortID := generateShortID(payload.ImageID)
	storeIDMapping(shortID, payload.ImageID)

	createContainerURL := generateCreateContainerURL(a, shortID)

	return sendJSONResponse(c, http.StatusOK, common.PushResponse{
		Success:            true,
		Message:            "Image pushed and imported successfully",
		CreateContainerURL: createContainerURL,
	})
}

// PostPushChunked handles chunked (large) image pushes
func PostPushChunked(c echo.Context, a *server.App) error {
	return handleChunkedPush(c, a, c.Request().Header.Get("X-Chunk-Metadata"))
}

func handleChunkedPush(c echo.Context, a *server.App, metadataStr string) error {
	var metadata ChunkMetadata
	if err := json.Unmarshal([]byte(metadataStr), &metadata); err != nil {
		return sendJSONResponse(c, http.StatusBadRequest, PushResponse{
			Success: false,
			Message: "Invalid chunk metadata",
		})
	}

	// Initialize chunk storage first
	chunkStore.mu.Lock()
	if chunkStore.chunks[metadata.TransferID] == nil {
		chunkStore.chunks[metadata.TransferID] = make(map[int][]byte)
		chunkStore.metadata[metadata.TransferID] = &metadata
	}
	chunkStore.mu.Unlock()

	chunkData, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return sendJSONResponse(c, http.StatusInternalServerError, PushResponse{
			Success: false,
			Message: "Failed to read chunk data",
		})
	}

	// Validate chunk data
	if err := validatePushChunk(chunkData, &metadata); err != nil {
		log.Error("Chunk validation failed",
			"error", err,
			"chunkNumber", metadata.ChunkNumber,
			"transferID", metadata.TransferID)
		return sendJSONResponse(c, http.StatusBadRequest, PushResponse{
			Success: false,
			Message: fmt.Sprintf("Invalid chunk: %v", err),
		})
	}

	// Store chunk data
	chunkStore.mu.Lock()
	chunkStore.chunks[metadata.TransferID][metadata.ChunkNumber] = chunkData
	chunkStore.mu.Unlock()

	// Process complete transfer if this was the last chunk
	if isTransferComplete(metadata.TransferID) {
		return processCompleteChunkedPushTransfer(c, a, metadata.TransferID)
	}

	// Acknowledge intermediate chunk
	return sendJSONResponse(c, http.StatusOK, PushResponse{
		Success: true,
		Message: fmt.Sprintf("Chunk %d/%d received successfully",
			metadata.ChunkNumber+1, metadata.TotalChunks),
	})
}

func processCompleteChunkedPushTransfer(c echo.Context, a *server.App, transferID string) error {
	log.Info("Starting complete push transfer processing",
		"transferID", transferID)

	// Get transfer data
	metadata, chunks, err := getTransferData(transferID)
	if err != nil {
		return sendPushErrorResponse(c, "Failed to get transfer data", err)
	}

	// Validate we have all chunks
	if len(chunks) != metadata.TotalChunks {
		return sendPushErrorResponse(c, "Incomplete transfer",
			fmt.Errorf("expected %d chunks, got %d", metadata.TotalChunks, len(chunks)))
	}

	// Create temporary directory with specific naming
	tmpDir, err := os.MkdirTemp("", "docker-push-*")
	if err != nil {
		return sendPushErrorResponse(c, "Failed to create temp directory", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create temporary file with .tar extension
	tmpFile, err := os.Create(filepath.Join(tmpDir, "image.tar"))
	if err != nil {
		return sendPushErrorResponse(c, "Failed to create temp file", err)
	}
	defer tmpFile.Close()

	// Write chunks in order to ensure proper tar assembly
	for i := 0; i < metadata.TotalChunks; i++ {
		chunk, exists := chunks[i]
		if !exists {
			return sendPushErrorResponse(c, "Missing chunk",
				fmt.Errorf("chunk %d not found", i))
		}
		if _, err := tmpFile.Write(chunk); err != nil {
			return sendPushErrorResponse(c, "Failed to write chunk",
				fmt.Errorf("error writing chunk %d: %v", i, err))
		}
	}

	// Ensure all data is written to disk
	if err := tmpFile.Sync(); err != nil {
		return sendPushErrorResponse(c, "Failed to sync file", err)
	}

	// Reset file pointer to beginning
	if _, err := tmpFile.Seek(0, 0); err != nil {
		return sendPushErrorResponse(c, "Failed to reset file pointer", err)
	}

	// Verify file size
	fileInfo, err := tmpFile.Stat()
	if err != nil {
		return sendPushErrorResponse(c, "Failed to get file info", err)
	}

	log.Info("Image file created",
		"size", fmt.Sprintf("%.2f MB", float64(fileInfo.Size())/(1024*1024)),
		"path", tmpFile.Name())

	imageName := c.Request().Header.Get("X-Image-Name")
	if imageName == "" {
		return sendPushErrorResponse(c, "Missing image name", fmt.Errorf("X-Image-Name header is required"))
	}

	log.Info("Importing image",
		"path", tmpFile.Name(),
		"imageName", imageName)

	// Import the image
	imageID, err := docker.ImportImageToEngine(tmpFile.Name())
	if err != nil {
		if strings.Contains(err.Error(), "platform compatibility check failed") {
			return sendJSONResponse(c, http.StatusBadRequest, PushResponse{
				Success: false,
				Message: fmt.Sprintf("Image platform not compatible with server: %v", err),
			})
		}
		return sendPushErrorResponse(c, "Failed to import image", err)
	}

	// Cleanup and prepare response
	cleanupTransfer(transferID)
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

func validatePushChunk(chunk []byte, metadata *ChunkMetadata) error {
	if len(chunk) == 0 {
		return errors.New("empty chunk received")
	}

	if metadata == nil {
		return errors.New("missing chunk metadata")
	}

	if int64(len(chunk)) != metadata.ChunkSize {
		return fmt.Errorf("chunk size mismatch: expected %d, got %d",
			metadata.ChunkSize, len(chunk))
	}

	return nil
}

func sendPushErrorResponse(c echo.Context, message string, err error) error {
	log.Error(message, "error", err)
	return sendJSONResponse(c, http.StatusInternalServerError, PushResponse{
		Success: false,
		Message: message,
	})
}

// validateAndPreparePushPayload validates and prepares the push payload
func validateAndPreparePushPayload(c echo.Context) (*common.PushPayload, error) {
	payload := &common.PushPayload{
		ImageName: c.Request().Header.Get("X-Image-Name"),
	}

	if payload.ImageName == "" {
		return nil, errors.New("invalid image name")
	}

	return payload, nil
}
