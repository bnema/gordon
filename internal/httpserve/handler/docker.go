package handler

import (
	"net/http"

	"github.com/bnema/gordon/pkg/utils/docker"
	"github.com/bnema/gordon/pkg/utils/sanitize"
	"github.com/labstack/echo/v4"
)

func ImageManagerHandler(c echo.Context) error {
	// Retrieve the list of container images.
	images, err := docker.ListContainerImages()
	if err != nil {
		rawErrHTML := `<div id="container-images">Error: ` + err.Error() + `</div>`
		sanitizedHTML, err := sanitize.SanitizeHTML(rawErrHTML)
		if err != nil {
			// Handle the sanitization error
			return c.String(http.StatusInternalServerError, "An error occurred during sanitization")
		}
		return c.HTML(http.StatusInternalServerError, sanitizedHTML)
	}

	// Create a map to hold the data you want to pass to the template.
	data := map[string]interface{}{
		"Images": images,
	}

	// Render the template with the data.
	return c.Render(http.StatusOK, "manager", data)
}
