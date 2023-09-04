package routes

import (
	"fmt"
	"net/http"
	"os"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/http/middlewares"
	"gogs.bnema.dev/gordon-echo/internal/ui"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

func AdminRoute(c echo.Context) error {
	// Retrieve the current language from the context
	lang := c.Get(middlewares.LangKey).(string)
	fmt.Println(lang)

	staticData, err := utils.PopulateDataFromYAML(lang)
	if err != nil {
		return err
	}

	renderer, err := utils.GetRenderer("index.gohtml", ui.PublicFS, utils.NewLogger())
	if err != nil {
		return err
	}

	html, err := renderer.Render(staticData.CurrentLang)
	if err != nil {
		// Return the error into NewLogger() to log it
		utils.NewLogger().Error()
		return err
	}

	return c.HTML(http.StatusOK, html)
}

func StaticRoute(c echo.Context) error {

	// Set the cache-control header to cache the static files for 1 day if PROD env is set to true

	if os.Getenv("PROD") == "true" {
		c.Response().Header().Set("Cache-Control", "public, max-age=86400")
	} else {
		c.Response().Header().Set("Cache-Control", "no-cache")
	}
	return echo.StaticDirectoryHandler(ui.PublicFS, false)(c)
}
