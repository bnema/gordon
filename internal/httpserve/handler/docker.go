package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/gotemplate/render"
	"github.com/bnema/gordon/pkg/utils/docker"
	"github.com/bnema/gordon/pkg/utils/humanize"
	"github.com/bnema/gordon/pkg/utils/sanitize"
	"github.com/docker/docker/api/types"
	"github.com/labstack/echo/v4"
)

var idMap = make(map[string]string)

type HumanReadableContainerImage struct {
	*types.ImageSummary
	Name       string
	ShortImgID string
	CreatedStr string
	SizeStr    string
}

type HumanReadableContainer struct {
	*types.Container
	CreatedStr string // Human-readable Created time
	SizeStr    string // Human-readable Size
	UpSince    string // Human-readable time elapsed since the container was started
	StateColor string // Color of the state badge
}

// ImageManagerHandler handles the /image-manager route
func ImageManagerHandler(c echo.Context, a *app.App) error {
	images, err := docker.ListContainerImages()
	if err != nil {
		rawErrHTML := `<div>Error: ` + err.Error() + `</div>`
		sanitizedHTML, err := sanitize.SanitizeHTML(rawErrHTML)
		if err != nil {
			return c.String(http.StatusInternalServerError, "An error occurred during sanitization")
		}
		return c.HTML(http.StatusInternalServerError, sanitizedHTML)
	}

	var humanReadableImages []HumanReadableContainerImage

	for _, image := range images {
		ShortImgID := image.ID[7:19]
		idMap[ShortImgID] = image.ID
		createdTime := time.Unix(image.Created, 0)
		createdStr := humanize.TimeAgo(createdTime)
		sizeStr := humanize.BytesToReadableSize(image.Size)
		for _, repoTag := range image.RepoTags {
			humanReadableImages = append(humanReadableImages, HumanReadableContainerImage{
				ShortImgID:   ShortImgID,
				ImageSummary: &image,
				CreatedStr:   createdStr,
				SizeStr:      sizeStr,
				Name:         repoTag,
			})
		}
	}
	data := map[string]interface{}{
		"Images": humanReadableImages,
	}

	rendererData, err := render.GetHTMLRenderer("html/fragments", "imagelist.gohtml", a.TemplateFS, a)
	if err != nil {
		return err
	}
	renderedHTML, err := rendererData.Render(data, a)
	if err != nil {
		return err
	}
	return c.HTML(200, renderedHTML)
}

// ImageManagerDeleteHandler handles the /image-manager/delete route
func ImageManagerDeleteHandler(c echo.Context, a *app.App) error {
	// Get the ShortImgID from the URL
	ShortImgID := c.Param("ShortImgID")
	fmt.Println("ShortImgID:", ShortImgID)
	// Check if the ShortImgID exists in the idMap
	imageID, exists := idMap[ShortImgID]
	fmt.Println("ImageID:", imageID)
	if !exists {
		return c.String(http.StatusBadRequest, "Invalid ShortImgID")
	}
	err := docker.DeleteContainerImage(imageID)
	if err != nil {
		c.Response().Header().Set("X-Error-Type", "image")
		return c.String(http.StatusInternalServerError, err.Error())

	}

	// Since it is HTMX we return a html div with the message "Removed"
	return c.HTML(http.StatusOK, `<div>Removed</div>`)
}

// ContainerManagerHandler handles the /container-manager route
func ContainerManagerHandler(c echo.Context, a *app.App) error {
	containers, err := docker.ListRunningContainers()
	if err != nil {
		rawErrHTML := `<div>Error: ` + err.Error() + `</div>`
		sanitizedHTML, err := sanitize.SanitizeHTML(rawErrHTML)
		if err != nil {
			return c.String(http.StatusInternalServerError, "An error occurred during sanitization")
		}
		return c.HTML(http.StatusInternalServerError, sanitizedHTML)
	}

	data := map[string]interface{}{
		"Containers": containers,
	}

	rendererData, err := render.GetHTMLRenderer("html/fragments", "containerlist.gohtml", a.TemplateFS, a)
	if err != nil {
		return err
	}
	renderedHTML, err := rendererData.Render(data, a)
	if err != nil {
		return err
	}
	return c.HTML(200, renderedHTML)
}

// ContainerManagerDeleteHandler handles the /container-manager/delete route
func ContainerManagerDeleteHandler(c echo.Context, a *app.App) error {
	// Get the ShortID from the URL
	ShortID := c.Param("ShortID")
	// Check if the shortID exists in the idMap
	containerID, exists := idMap[ShortID]
	if !exists {
		return c.String(http.StatusBadRequest, "Invalid shortID")
	}
	fmt.Println("ShortID:", ShortID, "ContainerID:", containerID)

	err := docker.DeleteContainer(containerID)
	if err != nil {
		rawErrHTML := `<div>Error: ` + err.Error() + `</div>`
		sanitizedHTML, err := sanitize.SanitizeHTML(rawErrHTML)
		if err != nil {
			// Handle the sanitization error
			return c.String(http.StatusInternalServerError, "An error occurred during sanitization")
		}
		return c.HTML(http.StatusInternalServerError, sanitizedHTML)
	}
	// Since it is HTMX we return a html div with the message "Removed"
	return c.HTML(http.StatusOK, `Success`)
}
