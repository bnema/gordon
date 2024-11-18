// endpoint_push.go
package handlers

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/verify"
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

	_, err = saveAndImportPushImage(c, a, payload)
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

	// Store chunk data for multi-chunk transfers
	chunkStore.mu.Lock()
	if chunkStore.chunks[metadata.TransferID] == nil {
		chunkStore.chunks[metadata.TransferID] = make(map[int][]byte)
		chunkStore.metadata[metadata.TransferID] = &metadata
	}

	chunkData, err := io.ReadAll(c.Request().Body)
	if err != nil {
		chunkStore.mu.Unlock()
		return sendJSONResponse(c, http.StatusInternalServerError, PushResponse{
			Success: false,
			Message: "Failed to read chunk data",
		})
	}

	// Store the chunk
	chunkStore.chunks[metadata.TransferID][metadata.ChunkNumber] = chunkData
	chunkStore.mu.Unlock()

	// If this is the last chunk, process the complete transfer
	if isTransferComplete(metadata.TransferID) {
		return processCompleteChunkedPushTransfer(c, a, metadata.TransferID)
	}

	// For intermediate chunks, just acknowledge receipt
	return sendJSONResponse(c, http.StatusOK, PushResponse{
		Success: true,
		Message: fmt.Sprintf("Chunk %d/%d received", metadata.ChunkNumber+1, metadata.TotalChunks),
	})
}

func processCompleteChunkedPushTransfer(c echo.Context, a *server.App, transferID string) error {
	log.Info("Starting complete push transfer processing", "transferID", transferID)

	imageName := c.Request().Header.Get("X-Image-Name")
	// Get transfer data
	metadata, chunks, err := getTransferData(transferID)
	if err != nil {
		return sendErrorResponse(c, "Failed to get transfer data", err)
	}

	// Create temporary file with .tar extension
	tmpDir, err := os.MkdirTemp("", "docker-import-*")
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

	// Verify the file is a valid tar archive
	if err := verify.VerifyTarFile(tmpFile.Name()); err != nil {
		return sendErrorResponse(c, "Invalid tar archive", err)
	}

	// Import the image
	log.Info("Importing image from file", "path", tmpFile.Name())
	imageID, err := docker.ImportImageToEngine(tmpFile.Name())
	if err != nil {
		if strings.Contains(err.Error(), "platform compatibility check failed") {
			log.Error("Platform compatibility check failed",
				"error", err,
				"imageName", imageName)
			return sendJSONResponse(c, http.StatusBadRequest, DeployResponse{
				Success: false,
				Message: fmt.Sprintf("Image platform not compatible with server. Server is running on %s/%s. Please use a compatible image.", runtime.GOOS, runtime.GOARCH),
			})
		}
		return sendErrorResponse(c, "Failed to import image", err)
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

// Helper functions
func getTransferData(transferID string) (*ChunkMetadata, map[int][]byte, error) {
	chunkStore.mu.Lock()
	defer chunkStore.mu.Unlock()

	metadata := chunkStore.metadata[transferID]
	if metadata == nil {
		return nil, nil, fmt.Errorf("transfer metadata not found")
	}
	return metadata, chunkStore.chunks[transferID], nil
}

func createTempFile() (*os.File, func(), error) {
	tmpDir, err := os.MkdirTemp("", "docker-import-*")
	if err != nil {
		return nil, nil, err
	}

	tmpFile, err := os.Create(filepath.Join(tmpDir, "image.tar"))
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, nil, err
	}

	cleanup := func() {
		tmpFile.Close()
		os.RemoveAll(tmpDir)
	}

	return tmpFile, cleanup, nil
}

func writeChunksToFile(file *os.File, chunks map[int][]byte, totalChunks int) error {
	// First, seek to the beginning of the file
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to seek to beginning: %w", err)
	}

	// Create a temporary buffer to hold all chunks
	tempBuffer := new(bytes.Buffer)

	// Write chunks in order
	for i := 0; i < totalChunks; i++ {
		chunk, exists := chunks[i]
		if !exists {
			return fmt.Errorf("missing chunk %d", i)
		}

		if _, err := tempBuffer.Write(chunk); err != nil {
			return fmt.Errorf("failed to write chunk %d to buffer: %w", i, err)
		}
	}

	// Write the complete buffer to file
	if _, err := io.Copy(file, tempBuffer); err != nil {
		return fmt.Errorf("failed to write buffer to file: %w", err)
	}

	// Ensure all data is written to disk
	if err := file.Sync(); err != nil {
		return fmt.Errorf("failed to sync file: %w", err)
	}

	// Reset file pointer to beginning
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to reset file pointer: %w", err)
	}

	return nil
}

func prepareFileForImport(file *os.File) error {
	// Reset to beginning of file
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to seek to beginning: %w", err)
	}

	// Verify the tar format
	tr := tar.NewReader(file)
	header, err := tr.Next()
	if err != nil {
		return fmt.Errorf("invalid tar format: %w", err)
	}

	// Check if this is a valid Docker image tar
	if header.Name == "manifest.json" {
		// Reset file pointer for subsequent reads
		_, err = file.Seek(0, 0)
		return err
	}

	return fmt.Errorf("file does not appear to be a valid Docker image archive")
}

func sendErrorResponse(c echo.Context, message string, err error) error {
	log.Error(message, "error", err)
	return sendJSONResponse(c, http.StatusInternalServerError, PushResponse{
		Success: false,
		Message: message,
	})
}
