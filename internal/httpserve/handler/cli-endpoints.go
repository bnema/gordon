package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/templating/cmdparams"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/store"
	"github.com/labstack/echo/v4"
)

type InfoResponse struct {
	Uptime  string `json:"uptime"`
	Version string `json:"version"`
}

type DeployResponse = common.DeployResponse
type PushResponse = common.PushResponse

func sendJSONResponse(c echo.Context, statusCode int, response interface{}) error {
	return c.JSON(statusCode, response)
}

func (info *InfoResponse) Populate(a *server.App) {
	info.Uptime = a.GetUptime()
	info.Version = a.GetVersionstring()
}

// Handle GET on /api/ping endpoint
func GetInfos(c echo.Context, a *server.App) error {
	payload := new(common.RequestPayload)
	if err := c.Bind(payload); err != nil {
		return c.JSON(http.StatusBadRequest, "Invalid payload: "+err.Error())
	}

	if payload.Type != "ping" {
		return c.JSON(http.StatusBadRequest, "Invalid payload type")
	}

	pingPayload, ok := payload.Payload.(common.PingPayload)
	if !ok {
		return c.JSON(http.StatusBadRequest, "Invalid payload structure")
	}

	if pingPayload.Message != "ping" {
		return c.JSON(http.StatusBadRequest, "Invalid ping message")
	}

	// Prepare and populate the information
	info := &InfoResponse{}
	info.Populate(a)

	return c.JSON(http.StatusOK, info)
}

func PostPush(c echo.Context, a *server.App) error {
	// Initialize pushPayload object
	payload := &common.PushPayload{
		ImageName: c.Request().Header.Get("X-Image-Name"),
	}

	if payload.ImageName == "" {
		return c.JSON(http.StatusBadRequest, "Invalid image name")
	}

	imageReader := c.Request().Body
	defer imageReader.Close()

	// Rename the image to a valid name so that it can be saved (remove user/, :tag and add .tar)
	imageFileName := payload.ImageName
	imageFileName = regexp.MustCompile(`^([a-zA-Z0-9\-_.]+\/)?`).ReplaceAllString(imageFileName, "")
	imageFileName = regexp.MustCompile(`(:[a-zA-Z0-9\-_.]+)?$`).ReplaceAllString(imageFileName, "")
	imageFileName = imageFileName + ".tar"

	// Save the image tar in the storage
	imagePath, err := store.SaveImageToStorage(&a.Config, imageFileName, imageReader)
	if err != nil {
		return sendJSONResponse(c, http.StatusInternalServerError, PushResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to save image: %v", err),
		})
	}

	// Debug
	fmt.Printf("Image saved successfully to: %s\n", imagePath)

	// Import the tar in docker
	imageID, err := docker.ImportImageToEngine(imagePath)
	if err != nil {
		store.RemoveFromStorage(imagePath) // Clean up the saved image if import fails
		return sendJSONResponse(c, http.StatusInternalServerError, PushResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to import image: %v", err),
		})
	}

	if imageID == "" {
		return sendJSONResponse(c, http.StatusInternalServerError, PushResponse{
			Success: false,
			Message: "Imported image ID is empty",
		})
	}

	// Remove the image from the storage
	err = store.RemoveFromStorage(imagePath)
	if err != nil {
		return sendJSONResponse(c, http.StatusInternalServerError, PushResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to remove temporary image file: %v", err),
		})
	}

	fmt.Printf("Image imported successfully: %s\n", payload.ImageName)

	// Update the payload with the image ID
	payload.ImageID = imageID

	// If we arrive here, send back a success response with the target domain
	return sendJSONResponse(c, http.StatusOK, common.PushResponse{
		Success: true,
		Message: "Deployment successful",
	})
}

func PostDeploy(c echo.Context, a *server.App) error {
	// Initialize pushPayload object
	payload := &common.DeployPayload{
		Port:         c.Request().Header.Get("X-Ports"),
		ImageName:    c.Request().Header.Get("X-Image-Name"),
		TargetDomain: c.Request().Header.Get("X-Target-Domain"),
	}

	if payload.ImageName == "" {
		return c.JSON(http.StatusBadRequest, "Invalid image name")
	}

	imageReader := c.Request().Body
	defer imageReader.Close()

	// Rename the image to a valid name so that it can be saved (remove user/, :tag and add .tar)
	imageFileName := payload.ImageName
	imageFileName = regexp.MustCompile(`^([a-zA-Z0-9\-_.]+\/)?`).ReplaceAllString(imageFileName, "")
	imageFileName = regexp.MustCompile(`(:[a-zA-Z0-9\-_.]+)?$`).ReplaceAllString(imageFileName, "")
	imageFileName = imageFileName + ".tar"

	// Check the ports struct, if there is no /tcp, or /udp, add /tcp
	if !regexp.MustCompile(`\/(tcp|udp)$`).MatchString(payload.Port) {
		payload.Port = payload.Port + "/tcp"
	}

	// Check the target domain, if there is no https:// or http://, add https://
	if !regexp.MustCompile(`^https?:\/\/`).MatchString(payload.TargetDomain) {
		payload.TargetDomain = "https://" + payload.TargetDomain
	}

	// Save the image tar in the storage
	imagePath, err := store.SaveImageToStorage(&a.Config, imageFileName, imageReader)
	if err != nil {
		return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to save image: %v", err),
		})
	}

	// Debug
	fmt.Printf("Image saved successfully to: %s\n", imagePath)

	// Import the tar in docker
	imageID, err := docker.ImportImageToEngine(imagePath)
	if err != nil {
		store.RemoveFromStorage(imagePath) // Clean up the saved image if import fails
		return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to import image: %v", err),
		})
	}

	if imageID == "" {
		return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
			Success: false,
			Message: "Imported image ID is empty",
		})
	}

	// Remove the image from the storage
	err = store.RemoveFromStorage(imagePath)
	if err != nil {
		return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to remove temporary image file: %v", err),
		})
	}

	fmt.Printf("Image imported successfully: %s\n", payload.ImageName)

	// Update the payload with the image ID
	payload.ImageID = imageID

	// Create the container using cmdparams.FromPayloadStructToCmdParams
	params, err := cmdparams.FromPayloadStructToCmdParams(payload, a, imageID)
	if err != nil {
		return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to create command parameters: %v", err),
		})
	}
	// Create the container
	containerID, err := docker.CreateContainer(params)
	if err != nil {
		return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to create container: %v", err),
		})
	}

	// Start the container
	err = docker.StartContainer(containerID)
	if err != nil {
		docker.RemoveContainer(containerID) // Clean up if start fails
		return sendJSONResponse(c, http.StatusInternalServerError, DeployResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to start container: %v", err),
		})
	}

	// If we arrive here, send back a success response with the target domain
	response := DeployResponse{
		Success: true,
		Message: "Deployment successful",
		Domain:  payload.TargetDomain,
	}

	// Log the response before sending
	responseJSON, _ := json.Marshal(response)
	fmt.Printf("Server response: %s\n", string(responseJSON))

	return c.JSON(http.StatusOK, response)
}
