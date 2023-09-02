package routes

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"gogs.bnema.dev/gordon-echo/internal/ui"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

func getPreferredLanguage(c echo.Context) string {
	header := c.Request().Header.Get("Accept-Language")
	// Check the header value, parse it and determine the primary language.
	// For this example, let's assume English and French only.
	if strings.Contains(header, "fr") {
		return "fr"
	}
	return "en" // Default to English
}

func AdminRoute(c echo.Context) error {
	staticData, err := utils.ReadDataFromYAML()
	if err != nil {
		return err
	}

	lang := getPreferredLanguage(c)
	fmt.Println(lang)

	switch lang {
	case "fr":
		staticData.CurrentLang = staticData.FR
	default: // default to English if no match
		staticData.CurrentLang = staticData.EN
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
	c.Response().Header().Set("Cache-Control", "public, max-age=86400")
	return echo.StaticDirectoryHandler(ui.PublicFS, false)(c)
}
