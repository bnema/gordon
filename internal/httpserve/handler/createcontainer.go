package handler

import (
	"fmt"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/gotemplate/render"
	"github.com/bnema/gordon/pkg/utils/docker"
	"github.com/bnema/gordon/pkg/utils/sanitize"
	"github.com/labstack/echo/v4"
)

type CreateContainer struct {
	ContainerName string
	ContainerHost string
	Environment   string
	ImageName     string
	ImageID       string
	Ports         string
	Volumes       string
	TraefikLabels []string
	Network       string
}

// FromShortIDToImageID converts a short image ID to a full image ID
func FromShortIDToImageID(ShortID string) (string, error) {
	idMapMutex.Lock()
	// Check if the ShortImgID exists in the idMap
	imageID, exists := idMap[ShortID]
	idMapMutex.Unlock()

	if !exists {
		return "", fmt.Errorf("Invalid ShortImgID")
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

	// Result : Form values: map[container_domain:gogs.localhost container_name:gogs image_id:sha256:5d92d8ae733d93dc4af5b32bb353539eeff660fc2136c6902abcdf3aafe4cef1 image_name:gogs/gogs:latest ports:8888:80 restart:unless-stopped volumes:/home/data/test:/data]
	// With those values we can use gotemplate/txt to create a custom docker run command
	cmdParams, err := render.FromInputsToCmdParams(sanitizedInputs) // Make sure cmdParams is of type render.ContainerCommandParams
	if err != nil {
		return sendError(c, err)
	}

	// Create the container
	err = docker.CreateContainer(cmdParams)
	if err != nil {
		return sendError(c, err)
	}

	return c.String(200, "Container created")
}
