// endpoint_helpers.go
package handlers

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/templating/cmdparams"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/store"
	"github.com/charmbracelet/log"
	"github.com/labstack/echo/v4"
)

type ChunkMetadata common.ChunkMetadata

type ChunkStore struct {
	mu       sync.Mutex
	chunks   map[string]map[int][]byte
	metadata map[string]*ChunkMetadata
	started  map[string]time.Time
}

type InfoResponse struct {
	Uptime  string `json:"uptime"`
	Version string `json:"version"`
}

type DeployResponse = common.DeployResponse
type PushResponse = common.PushResponse
type StopResponse = common.StopResponse
type RemoveResponse = common.RemoveResponse

var chunkStore = &ChunkStore{
	chunks:   make(map[string]map[int][]byte),
	metadata: make(map[string]*ChunkMetadata),
	started:  make(map[string]time.Time),
}

const (
	cleanupInterval = 5 * time.Minute
	transferTimeout = 30 * time.Minute
)

func init() {
	// Start a cleanup goroutine
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		for range ticker.C {
			cleanupOldTransfers()
		}
	}()
}

// Populate fills the InfoResponse struct with data from the App
func (info *InfoResponse) Populate(a *server.App) {
	info.Uptime = a.GetUptime()
	info.Version = a.GetVersionstring()
}

func sendJSONResponse(c echo.Context, statusCode int, response interface{}) error {
	err := c.JSON(statusCode, response)
	if err != nil {
		log.Error("Failed to send JSON response",
			"error", err,
			"statusCode", statusCode)
	}
	return err
}

// GetInfos handles GET requests on /api/ping endpoint
func GetInfos(c echo.Context, a *server.App) error {
	payload := new(common.RequestPayload)
	if err := c.Bind(payload); err != nil {
		return sendJSONResponse(c, http.StatusBadRequest, "Invalid payload: "+err.Error())
	}

	if payload.Type != "ping" {
		return sendJSONResponse(c, http.StatusBadRequest, "Invalid payload type")
	}

	pingPayload, ok := payload.Payload.(common.PingPayload)
	if !ok {
		return sendJSONResponse(c, http.StatusBadRequest, "Invalid payload structure")
	}

	if pingPayload.Message != "ping" {
		return sendJSONResponse(c, http.StatusBadRequest, "Invalid ping message")
	}

	info := &InfoResponse{}
	info.Populate(a)

	return sendJSONResponse(c, http.StatusOK, info)
}

// validateAndPrepareDeployPayload validates and prepares the deploy payload
func validateAndPrepareDeployPayload(c echo.Context) (*common.DeployPayload, error) {
	payload := &common.DeployPayload{
		Port:         c.Request().Header.Get("X-Ports"),
		ImageName:    c.Request().Header.Get("X-Image-Name"),
		TargetDomain: c.Request().Header.Get("X-Target-Domain"),
	}

	if payload.ImageName == "" {
		return nil, errors.New("invalid image name")
	}

	payload.Port = normalizePort(payload.Port)
	payload.TargetDomain = normalizeTargetDomain(payload.TargetDomain)

	return payload, nil
}

// ssaveAndImportImage saves and imports an image from the request body
func saveAndImportImage(c echo.Context, a *server.App, payload interface{}) (string, error) {
	var imageName, imageID string

	// Determine payload type and extract image name
	switch p := payload.(type) {
	case *common.PushPayload:
		imageName = p.ImageName
		defer func() { p.ImageID = imageID }()
	case *common.DeployPayload:
		imageName = p.ImageName
		defer func() { p.ImageID = imageID }()
	default:
		return "", fmt.Errorf("unsupported payload type: %T", payload)
	}

	imageReader := c.Request().Body
	defer imageReader.Close()

	imageFileName := sanitizeImageFileName(imageName)

	imagePath, err := store.SaveImageToStorage(&a.Config, imageFileName, imageReader)
	if err != nil {
		return "", fmt.Errorf("failed to save image: %v", err)
	}

	imageID, err = docker.ImportImageToEngine(imagePath)
	if err != nil {
		if removeErr := store.RemoveFromStorage(imagePath); removeErr != nil {
			log.Error("Failed to remove temporary image file after import failure",
				"error", removeErr,
				"path", imagePath)
		}
		return "", fmt.Errorf("failed to import image: %v", err)
	}

	if imageID == "" {
		if removeErr := store.RemoveFromStorage(imagePath); removeErr != nil {
			log.Error("Failed to remove temporary image file after empty ID",
				"error", removeErr,
				"path", imagePath)
		}
		return "", errors.New("imported image ID is empty")
	}

	err = store.RemoveFromStorage(imagePath)
	if err != nil {
		log.Error("Failed to remove temporary image file after successful import",
			"error", err,
			"path", imagePath)
		return "", fmt.Errorf("failed to remove temporary image file: %v", err)
	}

	return imagePath, nil
}

// createAndStartContainer creates a container from the image, starts it and returns the container ID
func createAndStartContainer(a *server.App, payload *common.DeployPayload) (string, string, error) {
	log.Info("Starting container creation process",
		"imageName", payload.ImageName,
		"imageID", payload.ImageID,
		"port", payload.Port)

	params, err := cmdparams.FromPayloadStructToCmdParams(payload, a, payload.ImageID)
	if err != nil {
		return "", "", fmt.Errorf("failed to create command parameters: %v", err)
	}

	log.Debug("Container parameters prepared", "params", params)

	containerID, err := docker.CreateContainer(params)
	if err != nil {
		return "", "", fmt.Errorf("failed to create container: %v", err)
	}

	log.Info("Container created successfully", "containerID", containerID)

	containerName, err := docker.GetContainerName(containerID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get container name: %v", err)
	}

	log.Info("Starting container",
		"containerID", containerID,
		"name", containerName)

	// Use regular StartContainer instead of StartContainerWithContext
	err = docker.StartContainer(containerID)
	if err != nil {
		// Get container logs if start failed
		logs, logErr := docker.GetContainerLogs(containerID)
		if logErr == nil {
			log.Error("Container start failed - container logs",
				"logs", logs,
				"containerID", containerID)
		}

		// Cleanup the failed container
		cleanupErr := docker.RemoveContainer(containerID)
		if cleanupErr != nil {
			log.Error("Failed to cleanup container after start failure",
				"containerID", containerID,
				"error", cleanupErr)
		}
		return "", "", fmt.Errorf("failed to start container: %v", err)
	}

	log.Info("Container started successfully",
		"containerID", containerID,
		"name", containerName)

	return containerID, containerName, nil
}

// Helpers for sanitizing and normalizing input
func sanitizeImageFileName(imageName string) string {
	imageName = regexp.MustCompile(`^([a-zA-Z0-9\-_.]+\/)?`).ReplaceAllString(imageName, "")
	imageName = regexp.MustCompile(`(:[a-zA-Z0-9\-_.]+)?$`).ReplaceAllString(imageName, "")
	return imageName + ".tar"
}

// normalizePort ensures that the port is in the format of "port/protocol"
func normalizePort(port string) string {
	if !regexp.MustCompile(`\/(tcp|udp)$`).MatchString(port) {
		return port + "/tcp"
	}
	return port
}

// normalizeTargetDomain ensures that the target domain is in the format of "https://domain" or "http://domain"
func normalizeTargetDomain(domain string) string {
	if !regexp.MustCompile(`^https?:\/\/`).MatchString(domain) {
		return "https://" + domain
	}
	return domain
}

// generateShortID generates a short ID from the full image ID
func generateShortID(imageID string) string {
	shortID := imageID[7:]
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	return shortID
}

// generateCreateContainerURL generates the URL for creating a container
func generateCreateContainerURL(a *server.App, shortID string) string {
	return fmt.Sprintf("%s://%s%s/cc/%s",
		a.Config.Http.Protocol(),
		a.Config.Http.FullDomain(),
		a.Config.Admin.Path,
		shortID)
}

// generateContainerLogsURL generates the URL for viewing container logs
func isTransferComplete(transferID string) bool {
	chunkStore.mu.Lock()
	defer chunkStore.mu.Unlock()

	metadata := chunkStore.metadata[transferID]
	if metadata == nil {
		log.Warn("No metadata found for transfer", "transferID", transferID)
		return false
	}

	chunksReceived := len(chunkStore.chunks[transferID])
	isComplete := chunksReceived == metadata.TotalChunks

	log.Debug("Transfer completion check",
		"transferID", transferID,
		"chunksReceived", chunksReceived,
		"totalExpected", metadata.TotalChunks,
		"isComplete", isComplete)

	return isComplete
}

// Cleanup helpers for old transfers
func cleanupTransfer(transferID string) {
	chunkStore.mu.Lock()
	defer chunkStore.mu.Unlock()

	delete(chunkStore.chunks, transferID)
	delete(chunkStore.metadata, transferID)
	delete(chunkStore.started, transferID)
}

// cleanupOldTransfers periodically checks for and cleans up old transfers
func cleanupOldTransfers() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop() // Good practice to clean up the ticker

	for range ticker.C {
		now := time.Now()
		chunkStore.mu.Lock()
		for id, startTime := range chunkStore.started {
			if now.Sub(startTime) > transferTimeout {
				log.Debug("Cleaning up stale transfer",
					"transferID", id,
					"age", now.Sub(startTime))
				cleanupTransfer(id)
			}
		}
		chunkStore.mu.Unlock()
	}
}

// extractContainerID extracts the container ID from an error message
func extractContainerID(errorMessage string) string {
	re := regexp.MustCompile(`by\s+([0-9a-f]+)\.`)
	match := re.FindStringSubmatch(errorMessage)
	if len(match) > 1 {
		return match[1]
	}
	return ""
}

// storeIDMapping stores a mapping between a short ID and a full ID
func storeIDMapping(shortID, fullID string) {
	safelyInteractWithIDMap(Update, shortID, fullID)
}

func writeChunksToFile(file *os.File, chunks map[int][]byte, totalChunks int) error {
	// Validate input parameters
	if file == nil {
		return errors.New("nil file handle provided")
	}
	if len(chunks) == 0 {
		return errors.New("no chunks provided")
	}
	if totalChunks <= 0 {
		return fmt.Errorf("invalid total chunks count: %d", totalChunks)
	}

	// Verify we have all chunks before starting
	if len(chunks) != totalChunks {
		return fmt.Errorf("chunk count mismatch: got %d, expected %d", len(chunks), totalChunks)
	}

	// Calculate total expected size
	var totalSize int64
	for _, chunk := range chunks {
		totalSize += int64(len(chunk))
	}

	log.Debug("Starting chunk assembly",
		"totalChunks", totalChunks,
		"totalSize", fmt.Sprintf("%.2f MB", float64(totalSize)/(1024*1024)))

	// First, seek to the beginning of the file
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to seek to beginning: %w", err)
	}

	// Create a temporary buffer with capacity hint
	tempBuffer := bytes.NewBuffer(make([]byte, 0, totalSize))

	// Write chunks in order with validation
	for i := 0; i < totalChunks; i++ {
		chunk, exists := chunks[i]
		if !exists {
			return fmt.Errorf("missing chunk %d", i)
		}

		if len(chunk) == 0 {
			return fmt.Errorf("empty chunk found at index %d", i)
		}

		n, err := tempBuffer.Write(chunk)
		if err != nil {
			return fmt.Errorf("failed to write chunk %d to buffer: %w", i, err)
		}
		if n != len(chunk) {
			return fmt.Errorf("incomplete chunk write: wrote %d of %d bytes for chunk %d",
				n, len(chunk), i)
		}
	}

	// Verify buffer size matches expected total
	if int64(tempBuffer.Len()) != totalSize {
		return fmt.Errorf("buffer size mismatch: got %d, expected %d",
			tempBuffer.Len(), totalSize)
	}

	// Write the complete buffer to file
	written, err := io.Copy(file, tempBuffer)
	if err != nil {
		return fmt.Errorf("failed to write buffer to file: %w", err)
	}
	if written != totalSize {
		return fmt.Errorf("file write size mismatch: wrote %d, expected %d",
			written, totalSize)
	}

	// Ensure all data is written to disk
	if err := file.Sync(); err != nil {
		return fmt.Errorf("failed to sync file: %w", err)
	}

	// Reset file pointer to beginning
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to reset file pointer: %w", err)
	}

	log.Debug("Chunk assembly completed successfully",
		"totalBytesWritten", written,
		"sizeInMB", fmt.Sprintf("%.2f MB", float64(written)/(1024*1024)))

	return nil
}

func getTransferData(transferID string) (*ChunkMetadata, map[int][]byte, error) {
	chunkStore.mu.Lock()
	defer chunkStore.mu.Unlock()

	metadata := chunkStore.metadata[transferID]
	if metadata == nil {
		return nil, nil, fmt.Errorf("transfer metadata not found")
	}
	return metadata, chunkStore.chunks[transferID], nil
}
