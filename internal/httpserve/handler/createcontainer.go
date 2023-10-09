package handler

import (
	"fmt"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/templates/render"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/sanitize"
	"github.com/labstack/echo/v4"
)

// FromShortIDToImageID converts a short image ID to a full image ID
func FromShortIDToImageID(ShortID string) (string, error) {
	idMapMutex.Lock()
	// Check if the ShortImgID exists in the idMap
	imageID, exists := idMap[ShortID]
	idMapMutex.Unlock()

	if !exists {
		return "", fmt.Errorf("image ID not found")
	}

	return imageID, nil
}

// CreateContainerRoute is the route for creating a new container
func CreateContainerGET(c echo.Context, a *app.App) error {
	// Retreive the ShortID of the image from the URL
	ShortID := c.Param("ID")

	// Convert the ShortID to a full image ID
	imageID, err := FromShortIDToImageID(ShortID)
	if err != nil {
		return sendError(c, err)
	}

	// Get the image info
	imageInfo, err := docker.GetImageInfo(imageID)
	if err != nil {
		return sendError(c, err)
	}

	// Extract image name
	var imageName string
	if len(imageInfo.RepoTags) > 0 {
		imageName = imageInfo.RepoTags[0] // Using the first tag as the image name
	}

	data := map[string]interface{}{
		"Title":     "Create a new container",
		"ShortID":   ShortID,
		"ImageID":   imageID,
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

func CreateContainerPOST(c echo.Context, a *app.App) error {
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

	cmdParams, err := render.FromInputsToCmdParams(sanitizedInputs, a)
	if err != nil {
		return sendError(c, err)
	}

	// Create the container
	_, err = docker.CreateContainer(cmdParams)
	if err != nil {
		return sendError(c, err)
	}

	return c.String(200, "Container created")
}
