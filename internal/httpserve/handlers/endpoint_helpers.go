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

// Helper functions for validation and preparation
func validateAndPreparePayload(c echo.Context) (*common.PushPayload, error) {
	payload := &common.PushPayload{
		ImageName: c.Request().Header.Get("X-Image-Name"),
	}

	if payload.ImageName == "" {
		return nil, errors.New("invalid image name")
	}

	return payload, nil
}

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

// Helper functions for saving and importing images
func saveAndImportImage(c echo.Context, a *server.App, payload *common.PushPayload) error {
	imageReader := c.Request().Body
	defer imageReader.Close()

	imageFileName := sanitizeImageFileName(payload.ImageName)

	imagePath, err := store.SaveImageToStorage(&a.Config, imageFileName, imageReader)
	if err != nil {
		return fmt.Errorf("failed to save image: %v", err)
	}

	imageID, err := docker.ImportImageToEngine(imagePath)
	if err != nil {
		store.RemoveFromStorage(imagePath)
		return fmt.Errorf("failed to import image: %v", err)
	}

	if imageID == "" {
		return errors.New("failed to import image")
	}

	err = store.RemoveFromStorage(imagePath)
	if err != nil {
		return fmt.Errorf("failed to remove temporary image file: %v", err)
	}

	payload.ImageID = imageID
	return nil
}

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

// Container management helpers
func createAndStartContainer(a *server.App, payload *common.DeployPayload) (string, string, error) {
	params, err := cmdparams.FromPayloadStructToCmdParams(payload, a, payload.ImageID)
	if err != nil {
		return "", "", fmt.Errorf("failed to create command parameters: %v", err)
	}

	containerID, err := docker.CreateContainer(params)
	if err != nil {
		return "", "", fmt.Errorf("failed to create container: %v", err)
	}

	containerName, err := docker.GetContainerName(containerID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get container name: %v", err)
	}

	err = docker.StartContainer(containerID)
	if err != nil {
		docker.RemoveContainer(containerID)
		return "", "", fmt.Errorf("failed to start container: %v", err)
	}

	return containerID, containerName, nil
}

// String manipulation helpers
func sanitizeImageFileName(imageName string) string {
	imageName = regexp.MustCompile(`^([a-zA-Z0-9\-_.]+\/)?`).ReplaceAllString(imageName, "")
	imageName = regexp.MustCompile(`(:[a-zA-Z0-9\-_.]+)?$`).ReplaceAllString(imageName, "")
	return imageName + ".tar"
}

func normalizePort(port string) string {
	if !regexp.MustCompile(`\/(tcp|udp)$`).MatchString(port) {
		return port + "/tcp"
	}
	return port
}

func normalizeTargetDomain(domain string) string {
	if !regexp.MustCompile(`^https?:\/\/`).MatchString(domain) {
		return "https://" + domain
	}
	return domain
}

func generateShortID(imageID string) string {
	shortID := imageID[7:]
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	return shortID
}

func generateCreateContainerURL(a *server.App, shortID string) string {
	return fmt.Sprintf("%s://%s%s/cc/%s",
		a.Config.Http.Protocol(),
		a.Config.Http.FullDomain(),
		a.Config.Admin.Path,
		shortID)
}

// Chunk management helpers
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

func cleanupTransfer(transferID string) {
	chunkStore.mu.Lock()
	defer chunkStore.mu.Unlock()

	delete(chunkStore.chunks, transferID)
	delete(chunkStore.metadata, transferID)
	delete(chunkStore.started, transferID)
}

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

// Error extraction helper
func extractContainerID(errorMessage string) string {
	re := regexp.MustCompile(`by\s+([0-9a-f]+)\.`)
	match := re.FindStringSubmatch(errorMessage)
	if len(match) > 1 {
		return match[1]
	}
	return ""
}

// File verification helper
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

func storeIDMapping(shortID, fullID string) {
	safelyInteractWithIDMap(Update, shortID, fullID)
}
