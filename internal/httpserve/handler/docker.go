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
	ShortID    string
	CreatedStr string
	SizeStr    string
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
		ShortID := image.ID[7:19]
		idMap[ShortID] = image.ID
		createdTime := time.Unix(image.Created, 0)
		createdStr := humanize.TimeAgo(createdTime)
		sizeStr := humanize.BytesToReadableSize(image.Size)

		humanReadableImages = append(humanReadableImages, HumanReadableContainerImage{
			ShortID:      ShortID,
			ImageSummary: &image,
			CreatedStr:   createdStr,
			SizeStr:      sizeStr,
		})
	}
	data := map[string]interface{}{
		"Images": humanReadableImages,
	}

	rendererData, err := render.GetHTMLRenderer("html/fragments", "image_list.gohtml", a.TemplateFS, a)
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
	// Get the ShortID from the URL
	ShortID := c.Param("ShortID")
	// Check if the shortID exists in the idMap
	imageID, exists := idMap[ShortID]
	if !exists {
		return c.String(http.StatusBadRequest, "Invalid shortID")
	}
	fmt.Println("ShortID:", ShortID, "ImageID:", imageID)

	err := docker.DeleteContainerImage(imageID)
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
	return c.HTML(http.StatusOK, `<div>Removed</div>`)
}
