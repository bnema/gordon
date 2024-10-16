package handlers

import (
	"net/http"

	"github.com/bnema/gordon/pkg/sanitize"
	"github.com/labstack/echo/v4"
)

func sendError(c echo.Context, err error) error {
	rawErrHTML := `<div>Error: ` + err.Error() + `</div>`
	sanitizedHTML, err := sanitize.SanitizeHTML(rawErrHTML)
	if err != nil {
		return c.String(http.StatusInternalServerError, "An error occurred during sanitization")
	}
	return c.HTML(http.StatusInternalServerError, sanitizedHTML)
}
