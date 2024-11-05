package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/templating/cmdparams"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/store"
	"github.com/charmbracelet/log"
	"github.com/labstack/echo/v4"
)

type InfoResponse struct {
	Uptime  string `json:"uptime"`
	Version string `json:"version"`
}

type DeployResponse = common.DeployResponse
type PushResponse = common.PushResponse
type StopResponse = common.StopResponse
type RemoveResponse = common.RemoveResponse

// sendJSONResponse is a helper function to send JSON responses
func sendJSONResponse(c echo.Context, statusCode int, response interface{}) error {
	// Debug
	log.Debug("Sending JSON response", "statusCode", statusCode, "response", response)
	return c.JSON(statusCode, response)
}

// Populate fills the InfoResponse struct with data from the App
func (info *InfoResponse) Populate(a *server.App) {
	info.Uptime = a.GetUptime()
	info.Version = a.GetVersionstring()
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

// PostContainerStop handles the container stop request
func PostContainerStop(c echo.Context, a *server.App) error {
	payload := new(common.RequestPayload)
	if err := c.Bind(payload); err != nil {
		return sendJSONResponse(c, http.StatusBadRequest, "Invalid payload: "+err.Error())
	}

	if payload.Type != "stop" {
		return sendJSONResponse(c, http.StatusBadRequest, "Invalid payload type")
	}

	stopPayload, ok := payload.Payload.(common.StopPayload)
	if !ok {
		return sendJSONResponse(c, http.StatusBadRequest, "Invalid payload structure")
	}

	if stopPayload.ContainerID == "" {
		return sendJSONResponse(c, http.StatusBadRequest, "Invalid container ID")
	}

	err := docker.StopContainer(stopPayload.ContainerID)
	if err != nil {
		return sendJSONResponse(c, http.StatusInternalServerError, StopResponse{
			Success: false,
			Message: err.Error(),
		})
	}

	return sendJSONResponse(c, http.StatusOK, StopResponse{
		Success: true,
		Message: "Container stopped successfully",
	})
}

// PostContainerRemove handles the container remove request
func PostContainerRemove(c echo.Context, a *server.App) error {
	payload := new(common.RequestPayload)
	if err := c.Bind(payload); err != nil {
		return sendJSONResponse(c, http.StatusBadRequest, "Invalid payload: "+err.Error())
	}

	if payload.Type != "remove" {
		return sendJSONResponse(c, http.StatusBadRequest, "Invalid payload type")
	}

	removePayload, ok := payload.Payload.(common.RemovePayload)
	if !ok {
		return sendJSONResponse(c, http.StatusBadRequest, "Invalid payload structure")
	}

	if removePayload.ContainerID == "" {
		return sendJSONResponse(c, http.StatusBadRequest, "Invalid container ID")
	}

	err := docker.RemoveContainer(removePayload.ContainerID)
	if err != nil {
		return sendJSONResponse(c, http.StatusInternalServerError, RemoveResponse{
			Success: false,
			Message: err.Error(),
		})
	}

	return sendJSONResponse(c, http.StatusOK, RemoveResponse{
		Success: true,
		Message: "Container removed successfully",
	})
}

// PostPush handles the image push request
func PostPush(c echo.Context, a *server.App) error {
	payload, err := validateAndPreparePayload(c)
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

// validateAndPreparePayload checks the request and prepares the payload
func validateAndPreparePayload(c echo.Context) (*common.PushPayload, error) {
	payload := &common.PushPayload{
		ImageName: c.Request().Header.Get("X-Image-Name"),
	}

	if payload.ImageName == "" {
		return nil, errors.New("invalid image name")
	}

	return payload, nil
}

// saveAndImportImage saves the image to storage and imports it to the Docker engine
func saveAndImportImage(c echo.Context, a *server.App, payload *common.PushPayload) (string, error) {
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

// sanitizeImageFileName removes unwanted parts from the image name
func sanitizeImageFileName(imageName string) string {
	imageName = regexp.MustCompile(`^([a-zA-Z0-9\-_.]+\/)?`).ReplaceAllString(imageName, "")
	imageName = regexp.MustCompile(`(:[a-zA-Z0-9\-_.]+)?$`).ReplaceAllString(imageName, "")
	return imageName + ".tar"
}

// generateShortID creates a shortened version of the image ID
func generateShortID(imageID string) string {
	shortID := imageID[7:]
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	return shortID
}

// storeIDMapping saves the mapping between short ID and full ID
func storeIDMapping(shortID, fullID string) {
	safelyInteractWithIDMap(Update, shortID, fullID)
}

// generateCreateContainerURL creates the URL for the container creation page
func generateCreateContainerURL(a *server.App, shortID string) string {
	return fmt.Sprintf("%s://%s%s/cc/%s",
		a.Config.Http.Protocol(),
		a.Config.Http.FullDomain(),
		a.Config.Admin.Path,
		shortID)
}

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

// extractContainerID extracts the container ID from the error message
func extractContainerID(errorMessage string) string {
	re := regexp.MustCompile(`by\s+([0-9a-f]+)\.`)
	match := re.FindStringSubmatch(errorMessage)
	if len(match) > 1 {
		return match[1]
	}
	return ""
}

// validateAndPrepareDeployPayload prepares the deployment payload
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

// normalizePort adds "/tcp" to the port if not present
func normalizePort(port string) string {
	if !regexp.MustCompile(`\/(tcp|udp)$`).MatchString(port) {
		return port + "/tcp"
	}
	return port
}

// normalizeTargetDomain adds "https://" to the domain if not present
func normalizeTargetDomain(domain string) string {
	if !regexp.MustCompile(`^https?:\/\/`).MatchString(domain) {
		return "https://" + domain
	}
	return domain
}

// saveAndImportDeployImage saves and imports the deployment image
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

// createAndStartContainer creates and starts a new container
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

// logResponse logs the deployment response
func logResponse(response DeployResponse) {
	responseJSON, _ := json.Marshal(response)
	log.Info("Deployment response", string(responseJSON))
}
