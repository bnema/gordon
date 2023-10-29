package handler

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/server"
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
	body, _ := io.ReadAll(c.Request().Body)
	c.Request().Body = io.NopCloser(bytes.NewBuffer(body)) // Reset the body

	payload := new(common.RequestPayload)
	if err := c.Bind(payload); err != nil {
		fmt.Println("Bind Error:", err)
		return c.JSON(http.StatusBadRequest, err.Error())
	}

	if payload.Type != "push" {
		return c.JSON(http.StatusBadRequest, "Invalid payload type")
	}

	pushPayload, ok := payload.Payload.(common.PushPayload)
	if !ok {
		return c.JSON(http.StatusBadRequest, "Invalid payload type")
	}

	// Push payload contains Ports as a string, Image as string (image:tag), and the .tar image as a byte array
	// Validate the ports
	ports := pushPayload.Ports
	if ports == "" {
		return c.JSON(http.StatusBadRequest, "Ports cannot be empty")
	}

	// Validate the target domain
	domain := pushPayload.TargetDomain
	if domain == "" {
		return c.JSON(http.StatusBadRequest, "Image cannot be empty")
	}

	// Validate the image tar
	imageTar := pushPayload.Data
	if imageTar == nil {
		return c.JSON(http.StatusBadRequest, "Image tar cannot be empty")
	}

	imageName := pushPayload.ImageName
	if imageName == "" {
		return c.JSON(http.StatusBadRequest, "Image name cannot be empty")
	}

	// 1 - Save the image tar in the storage
	imagePath, err := store.SaveImageToStorage(&a.Config, imageName, bytes.NewBuffer(imageTar))
	if err != nil {
		return c.JSON(http.StatusBadRequest, err.Error())
	}
	// 2 - Import the tar in docker
	imageID, err := docker.ImportImageToEngine(imagePath)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err.Error())
	}

	fmt.Println("Import successful, ImageID:", imageID)

	// 3 - Create the container with traefik labels

	// 4 - Start the container

	return c.JSON(http.StatusOK, "OK")
}
