package handler

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/templating/cmdparams"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/store"
	"github.com/labstack/echo/v4"
)

// Handle GET on /api/hello endpoint
func GetHello(c echo.Context, a *server.App) error {
	return c.JSON(http.StatusOK, "Hello, World!")
}

type InfoResponse struct {
	Uptime  string `json:"uptime"`
	Version string `json:"version"`
}

func (info *InfoResponse) Populate(a *server.App) {
	info.Uptime = a.GetUptime()
	info.Version = a.GetVersionstring()
}

// Handle GET on /api/ping endpoint
func GetInfos(c echo.Context, a *server.App) error {
	body, _ := io.ReadAll(c.Request().Body)
	c.Request().Body = io.NopCloser(bytes.NewBuffer(body)) // Reset the body

	payload := new(common.RequestPayload)
	if err := c.Bind(payload); err != nil {
		fmt.Println("Bind Error:", err)
		return c.JSON(http.StatusBadRequest, err.Error())
	}

	if payload.Type != "ping" {
		return c.JSON(http.StatusBadRequest, "Invalid payload type")
	}

	pingPayload, ok := payload.Payload.(common.PingPayload)
	if !ok {
		return c.JSON(http.StatusBadRequest, "Invalid payload type")
	}
	if pingPayload.Message != "ping" {
		return c.JSON(http.StatusBadRequest, "Invalid payload data")
	}

	// Prepare and populate the information
	info := &InfoResponse{}
	info.Populate(a)

	return c.JSON(http.StatusOK, info)

}

func PostPush(c echo.Context, a *server.App) error {

	// Initialize pushPayload object
	payload := &common.PushPayload{
		Ports:        c.Request().Header.Get("X-Ports"),
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

	// Save the image tar in the storage
	imageFilePath, err := store.SaveImageToStorage(&a.Config, imageFileName, imageReader)
	if err != nil {
		return sendJsonError(c, err)
	}

	// Import the tar in docker
	err = docker.ImportImageToEngine(imageFilePath)
	if err != nil {
		return sendJsonError(c, err)
	}

	// Remove the image from the storage
	err = store.RemoveFromStorage()
	if err != nil {
		return sendJsonError(c, err)
	}

	// append localhost to the image name since it's a manually imported image and not from a registry
	payload.ImageName = "localhost/" + payload.ImageName

	// Get the image ID
	imageID, err := docker.GetImageID(payload.ImageName)
	if err != nil {
		return sendJsonError(c, err)
	}

	// Update the payload with the image ID
	payload.ImageID = imageID

	// Create the container using cmdparams.FromPayloadStructToCmdParams
	params, err := cmdparams.FromPayloadStructToCmdParams(payload, a)
	if err != nil {
		return sendJsonError(c, err)
	}

	// Create the container
	containerID, err := docker.CreateContainer(params)
	if err != nil {
		return sendJsonError(c, err)
	}

	// Start the container
	err = docker.StartContainer(containerID)
	if err != nil {
		return sendJsonError(c, err)
	}

	// Remove the container if it fails to start
	defer func() {
		if err != nil {
			docker.RemoveContainer(containerID)
			c.JSON(http.StatusInternalServerError, sendJsonError(c, err))
			return
		}
	}()

	// If we arrive here, send back payload.TargetDomain so the client can test it
	return c.JSON(http.StatusOK, payload.TargetDomain)
}
