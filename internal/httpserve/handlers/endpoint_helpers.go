// endpoint_helpers.go
package handlers

import (
	"errors"
	"fmt"
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

// saveAndImportPushImage saves the image to the storage and imports it to the Docker engine, updating the payload with the image ID and returning the path to the saved image
func saveAndImportPushImage(c echo.Context, a *server.App, payload *common.PushPayload) (string, error) {
	imageReader := c.Request().Body
	defer imageReader.Close()

	imageFileName := sanitizeImageFileName(payload.ImageName)

	imagePath, err := store.SaveImageToStorage(&a.Config, imageFileName, imageReader)
	if err != nil {
		return "", fmt.Errorf("failed to save image: %v", err)
	}

	imageID, err := docker.ImportImageToEngine(imagePath)
	if err != nil {
		store.RemoveFromStorage(imagePath)
		return "", fmt.Errorf("failed to import image: %v", err)
	}

	if imageID == "" {
		return "", errors.New("imported image ID is empty")
	}

	err = store.RemoveFromStorage(imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to remove temporary image file: %v", err)
	}

	payload.ImageID = imageID
	return imagePath, nil
}

// saveAndImportDeployImage saves the image to the storage and imports it to the Docker engine, updating the payload with the image ID and returning the path to the saved image
func saveAndImportDeployImage(c echo.Context, a *server.App, payload *common.DeployPayload) (string, error) {

	imageReader := c.Request().Body
	defer imageReader.Close()

	imageFileName := sanitizeImageFileName(payload.ImageName)

	imagePath, err := store.SaveImageToStorage(&a.Config, imageFileName, imageReader)
	if err != nil {
		return "", fmt.Errorf("failed to save image: %v", err)
	}

	imageID, err := docker.ImportImageToEngine(imagePath)
	if err != nil {
		store.RemoveFromStorage(imagePath)
		return "", fmt.Errorf("failed to import image: %v", err)
	}

	if imageID == "" {
		return "", errors.New("imported image ID is empty")
	}

	err = store.RemoveFromStorage(imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to remove temporary image file: %v", err)
	}

	payload.ImageID = imageID
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

// verifyFileContents checks if the file size matches the expected size
func verifyFileContents(file *os.File, expectedSize int64) error {
	actualSize, err := file.Seek(0, 2) // Seek to end
	if err != nil {
		return fmt.Errorf("failed to verify file size: %w", err)
	}

	if actualSize != expectedSize {
		return fmt.Errorf("final file size mismatch: got %d, expected %d", actualSize, expectedSize)
	}

	_, err = file.Seek(0, 0) // Reset to beginning
	return err
}

// storeIDMapping stores a mapping between a short ID and a full ID
func storeIDMapping(shortID, fullID string) {
	safelyInteractWithIDMap(Update, shortID, fullID)
}
