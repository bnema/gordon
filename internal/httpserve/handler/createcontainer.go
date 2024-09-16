package handler

import (
	"fmt"
	"log"
	"net/http"

	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/templating/cmdparams"
	"github.com/bnema/gordon/internal/templating/render"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/sanitize"
	"github.com/labstack/echo/v4"
)

// FromShortIDToImageID converts a short image ID to a full image ID
func FromShortIDToImageID(ShortID string) (string, error) {
	imageID, exists := safelyInteractWithIDMap(Fetch, ShortID)
	if !exists {
		log.Printf("No mapping found for short ID: %s", ShortID)
		return "", fmt.Errorf("image ID not found")
	}
	return imageID, nil
}

// CreateContainerRoute is the route for creating a new container
func CreateContainerGET(c echo.Context, a *server.App) error {
	log.Printf("Full request URL: %s", c.Request().URL.String())
	// Retrieve the ShortID of the image from the URL
	shortID := c.Param("ID")
	log.Printf("Received ShortID: %s", shortID)

	fullID, exists := safelyInteractWithIDMap(Fetch, shortID)
	if !exists {
		log.Printf("No mapping found for short ID: %s", shortID)
		return c.String(http.StatusNotFound, "Image ID not found")
	}

	log.Printf("Found mapping: %s -> %s", shortID, fullID)

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
	log.Printf("Received ShortID: %s", shortID)

	fullID, exists := safelyInteractWithIDMap(Fetch, shortID)
	if !exists {
		log.Printf("No mapping found for short ID: %s", shortID)
		return c.String(http.StatusNotFound, "Image ID not found")
	}

	log.Printf("Found mapping: %s -> %s", shortID, fullID)

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
		log.Printf("Error rendering template: %v", err)
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
	_, err = docker.CreateContainer(cmdParams)
	if err != nil {
		return sendError(c, err)
	}

	return c.HTML(http.StatusOK, ActionSuccess(a))
}
