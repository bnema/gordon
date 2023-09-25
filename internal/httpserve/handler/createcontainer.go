package handler

import (
	"net/http"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/gotemplate/render"
	"github.com/bnema/gordon/pkg/utils/docker"
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

// CreateContainerRoute is the route for creating a new container
func CreateContainerGETHandler(c echo.Context, a *app.App) error {
	// Retreive the image ID from the URL
	ShortID := c.Param("ID")
	idMapMutex.Lock()
	// Check if the ShortImgID exists in the idMap
	imageID, exists := idMap[ShortID]
	idMapMutex.Unlock()

	imageInfo, err := docker.GetImageInfo(imageID)
	if err != nil {
		return sendError(c, err)
	}
	// Extract image name
	var imageName string
	if len(imageInfo.RepoTags) > 0 {
		imageName = imageInfo.RepoTags[0] // Using the first tag as the image name
	}

	if !exists {
		return c.String(http.StatusBadRequest, "Invalid ShortImgID")
	}

	data := map[string]interface{}{
		"Title":     "Create a new container",
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
