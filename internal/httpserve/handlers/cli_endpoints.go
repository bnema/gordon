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
		Message:            "Image pushed successfully",
		CreateContainerURL: createContainerURL,
	})
}

// validateAndPreparePayload checks the request and prepares the payload
func validateAndPreparePayload(c echo.Context) (*common.PushPayload, error) {
	payload := &common.PushPayload{
		ImageName: c.Request().Header.Get("X-Image-Name"),
	}

	if payload.ImageName == "" {
		return nil, errors.New("Invalid image name")
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
		return "", fmt.Errorf("Failed to save image: %v", err)
	}

	imageID, err := docker.ImportImageToEngine(imagePath)
	if err != nil {
		store.RemoveFromStorage(imagePath)
		return "", fmt.Errorf("Failed to import image: %v", err)
	}

	if imageID == "" {
		return "", errors.New("Imported image ID is empty")
	}

	err = store.RemoveFromStorage(imagePath)
	if err != nil {
		return "", fmt.Errorf("Failed to remove temporary image file: %v", err)
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

	containerID, err := createAndStartContainer(a, payload)
	if err != nil {
		if strings.Contains(err.Error(), "is already in use") {
			log.Warn("Container already exists", "error", err)
			extractedID := extractContainerID(err.Error())
			log.Info("Extracted container ID", "containerID", extractedID)

			if extractedID == "" {
				log.Error("Failed to extract container ID from error message")
				return sendJSONResponse(c, http.StatusConflict, DeployResponse{
					Success: false,
					Message: "A container with this name already exists, but we couldn't extract its ID.",
				})
			}

			return sendJSONResponse(c, http.StatusConflict, DeployResponse{
				Success:     false,
				Message:     "A container for this deploy already exists.",
				ContainerID: extractedID,
			})
		}
		log.Error("Failed to create or start container", "error", err)
		return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to create or start container: %v", err),
		})
	}
	response := DeployResponse{
		Success:     true,
		Message:     "Deployment successful",
		Domain:      payload.TargetDomain,
		ContainerID: containerID,
	}

	// Debug: Verify containerID in response
	if response.ContainerID != containerID {
		log.Error("Container ID mismatch", "responseID", response.ContainerID, "actualID", containerID)
	} else {
		log.Info("Container ID successfully stored in response", "containerID", containerID)
	}

	return sendJSONResponse(c, http.StatusOK, response)
}

// is already in use by 08433f639cb1c01254d43f234360bfa793aa8c33b29e8de8ce42b0706c0090ba. example
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
		return nil, errors.New("Invalid image name")
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
		return "", fmt.Errorf("Failed to save image: %v", err)
	}

	imageID, err := docker.ImportImageToEngine(imagePath)
	if err != nil {
		store.RemoveFromStorage(imagePath)
		return "", fmt.Errorf("Failed to import image: %v", err)
	}

	if imageID == "" {
		return "", errors.New("Imported image ID is empty")
	}

	err = store.RemoveFromStorage(imagePath)
	if err != nil {
		return "", fmt.Errorf("Failed to remove temporary image file: %v", err)
	}

	payload.ImageID = imageID
	return imagePath, nil
}

// createAndStartContainer creates and starts a new container
func createAndStartContainer(a *server.App, payload *common.DeployPayload) (string, error) {
	params, err := cmdparams.FromPayloadStructToCmdParams(payload, a, payload.ImageID)
	if err != nil {
		return "", fmt.Errorf("Failed to create command parameters: %v", err)
	}

	containerID, err := docker.CreateContainer(params)
	if err != nil {
		return "", fmt.Errorf("Failed to create container: %v", err)
	}

	err = docker.StartContainer(containerID)
	if err != nil {
		docker.RemoveContainer(containerID)
		return "", fmt.Errorf("Failed to start container: %v", err)
	}

	return containerID, nil
}

// logResponse logs the deployment response
func logResponse(response DeployResponse) {
	responseJSON, _ := json.Marshal(response)
	fmt.Printf("Server response: %s\n", string(responseJSON))
}
