package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/templating/cmdparams"
	"github.com/bnema/gordon/internal/templating/render"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/sanitize"
	"github.com/charmbracelet/log"
	"github.com/labstack/echo/v4"
)

// FromShortIDToImageID converts a short image ID to a full image ID
func FromShortIDToImageID(ShortID string) (string, error) {
	imageID, exists := safelyInteractWithIDMap(Fetch, ShortID)
	if !exists {
		log.Debug("No mapping found for short ID: %s", ShortID)
		return "", fmt.Errorf("image ID not found")
	}
	return imageID, nil
}

// CreateContainerRoute is the route for creating a new container
func CreateContainerGET(c echo.Context, a *server.App) error {
	log.Debug("Full request URL: %s", c.Request().URL.String())
	// Retrieve the ShortID of the image from the URL
	shortID := c.Param("ID")
	log.Debug("Received ShortID: %s", shortID)

	fullID, exists := safelyInteractWithIDMap(Fetch, shortID)
	if !exists {
		log.Debug("No mapping found for short ID: %s", shortID)
		return c.String(http.StatusNotFound, "Image ID not found")
	}

	log.Debug("Found mapping: %s -> %s", shortID, fullID)

	// Get the image info
	imageInfo, err := docker.GetImageInfo(fullID)
	if err != nil {
		return sendError(c, err)
	}

	// Extract image name
	var imageName string
	if len(imageInfo.RepoTags) > 0 {
		imageName = imageInfo.RepoTags[0]
	}

	data := map[string]interface{}{
		"Title":     "Create a new container",
		"ShortID":   shortID,
		"ImageID":   fullID,
		"ImageName": imageName,
	}

	// Render the create container page
	rendererData, err := render.GetHTMLRenderer("html/fragments", "createcontainer.gohtml", a.TemplateFS, a)

	if err != nil {
		return sendError(c, err)
	}

	renderedHTML, err := rendererData.Render(data, a)

	if err != nil {
		return sendError(c, err)
	}

	return c.HTML(200, renderedHTML)

}

// CreateContainerFullGET handles the full HTML page for creating a new container
func CreateContainerFullGET(c echo.Context, a *server.App) error {
	// Retrieve the ShortID of the image from the URL
	shortID := c.Param("ID")
	log.Debug("Received ShortID: %s", shortID)

	fullID, exists := safelyInteractWithIDMap(Fetch, shortID)
	if !exists {
		log.Debug("No mapping found for short ID: %s", shortID)
		return c.String(http.StatusNotFound, "Image ID not found")
	}

	log.Debug("Found mapping: %s -> %s", shortID, fullID)

	// Get the image info
	imageInfo, err := docker.GetImageInfo(fullID)
	if err != nil {
		return sendError(c, err)
	}

	// Extract image name
	var imageName string
	if len(imageInfo.RepoTags) > 0 {
		imageName = imageInfo.RepoTags[0]
	}

	data := map[string]interface{}{
		"Title":     "Create a new container",
		"ShortID":   shortID,
		"ImageID":   fullID,
		"ImageName": imageName,
		"AdminPath": a.Config.Admin.Path,
	}

	// Specify both admin and fragments directories
	rendererData, err := render.GetHTMLRenderer("html/admin", "createcontainerfull.gohtml", a.TemplateFS, a, "html/fragments")
	if err != nil {
		return sendError(c, err)
	}

	renderedHTML, err := rendererData.Render(data, a)
	if err != nil {
		log.Debug("Error rendering template: %v", err)
		return sendError(c, err)
	}

	return c.HTML(200, renderedHTML)
}

// CreateContainerPOST handles the create container form submission
func CreateContainerPOST(c echo.Context, a *server.App) error {
	// Retreive the ShortID of the image from the URL
	ShortID := c.Param("ID")

	// Convert the ShortID to a full image ID
	imageID, err := FromShortIDToImageID(ShortID)
	if err != nil {
		return sendError(c, err)
	}

	// Get the image info
	_, err = docker.GetImageInfo(imageID)
	if err != nil {
		return sendError(c, err)
	}

	// Retrieve all form parameters
	formParams, err := c.FormParams()
	if err != nil {
		return sendError(c, err)
	}

	// Initialize a map to store sanitized values
	sanitizedInputs := make(map[string]string)

	// Iterate over all form parameters and sanitize them
	for k, v := range formParams {
		if len(v) > 0 {
			sanitized, err := sanitize.SanitizeHTML(v[0])
			if err != nil {
				return sendError(c, err)
			}
			sanitizedInputs[k] = sanitized
		}
	}

	cmdParams, err := cmdparams.FromInputsToCmdParams(sanitizedInputs, a)
	if err != nil {
		return sendError(c, err)
	}

	// Create the container
	containerID, err := docker.CreateContainer(cmdParams)
	if err != nil {
		return sendError(c, err)
	}

	// Start the container
	err = docker.StartContainer(containerID)
	if err != nil {
		// If start fails, try to cleanup the container
		log.Warn("Failed to start container", "error", err)
		_ = docker.RemoveContainer(containerID)
		return sendError(c, fmt.Errorf("failed to start container: %w", err))
	}

	// Get the container name for logging and as fallback
	containerName, err := docker.GetContainerName(containerID)
	if err != nil {
		log.Warn("Failed to get container name", "error", err)
	} else {
		containerName = strings.TrimPrefix(containerName, "/")
	}

	// Get container IP
	containerIP := GetContainerIP(a, containerID, containerName)

	// Construct target domain from form inputs
	targetDomain := fmt.Sprintf("%s://%s.%s",
		sanitizedInputs["container_protocol"],
		sanitizedInputs["container_subdomain"],
		sanitizedInputs["container_domain"])

	// Add proxy route for the new container
	err = AddProxyRoute(a, containerID, containerIP, sanitizedInputs["proxy_port"], targetDomain)
	if err != nil {
		log.Error("Failed to add proxy route", "error", err)
		// Continue anyway, don't fail the whole operation
	}

	return c.HTML(http.StatusOK, ActionSuccess(a))
}
