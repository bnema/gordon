package main

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/ui"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

const trailedAdminPath = "/admin/"

// bindStaticAdminUI serves the static admin UI
func bindStaticAdminUI(e *echo.Echo) error {
	tmpl, err := template.New("index").ParseFS(ui.PublicFS, "index.html")
	if err != nil {
		return fmt.Errorf("failed to parse index.html: %w", err)
	}

	renderer := &utils.Renderer{
		Template:   tmpl,
		ParseError: err,
	}

	// redirect to trailing slash to ensure that relative urls will still work properly
	e.GET(
		strings.TrimRight(trailedAdminPath, "/"),
		func(c echo.Context) error {
			return c.Redirect(http.StatusTemporaryRedirect, strings.TrimLeft(trailedAdminPath, "/"))
		},
	)

	// Main handler for /admin/
	e.GET(trailedAdminPath, func(c echo.Context) error {
		data := map[string]interface{}{
			"website": map[string]interface{}{
				"title": "Hello, World!",
			},
		}

		html, err := renderer.Render(data)
		if err != nil {
			return c.String(http.StatusInternalServerError, fmt.Sprintf("Failed to render template: %v", err))
		}

		return c.HTML(http.StatusOK, html)
	})
	// serves static files from the /ui/dist directory
	// (similar to echo.StaticFS but with gzip middleware enabled)
	e.GET(
		trailedAdminPath+"*",
		echo.StaticDirectoryHandler(ui.PublicFS, false),
	)
	e.GET("/htmx", func(c echo.Context) error {
		fragment := c.Request().Header.Get("X-Fragment")

		if fragment == "hello" {
			// Fetch and return the "hello" fragment
			content, err := utils.GetHTMLFragmentByID("hello")
			fmt.Println("X-Fragment:", c.Request().Header.Get("X-Fragment"))
			if err != nil {
				return c.String(http.StatusInternalServerError, err.Error())
			}
			return c.HTML(http.StatusOK, content)
		}

		// Handle other fragments or default behavior
		return c.String(http.StatusBadRequest, "Invalid fragment")
	})
	return nil
}

func main() {
	e := echo.New()

	// use func bindStaticAdminUI to serve the static admin UI
	bindStaticAdminUI(e)

	e.Logger.Fatal(e.Start(":1323"))

}
